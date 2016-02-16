package mirror

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/codegangsta/cli"
	"github.com/vulcand/oxy/utils"
	"github.com/vulcand/vulcand/plugin"
)

const (
	Type                               = "mirror"
	DefaultTimeoutDuration             = 30 * time.Second
	DefaultKeepAliveDuration           = 30 * time.Second
	DefaultTLSHandshakeTimeoutDuration = 10 * time.Second
)

func GetSpec() *plugin.MiddlewareSpec {
	return &plugin.MiddlewareSpec{
		Type:      Type,
		FromOther: FromOther,
		FromCli:   FromCli,
		CliFlags:  CliFlags(),
	}
}

type Config struct {
	Scheme              string
	Host                string
	Timeout             time.Duration
	KeepAlive           time.Duration
	TLSHandshakeTimeout time.Duration
	Connections         int64
	Variable            string
}

type Handler struct {
	cfg     Config
	next    http.Handler
	proxy   *httputil.ReverseProxy
	limiter *Limiter
}

type Limiter struct {
	mutex            *sync.Mutex
	extract          utils.SourceExtractor
	connections      map[string]int64
	maxConnections   int64
	totalConnections int64
}

func (l *Limiter) acquire(token string, amount int64) error {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	connections := l.connections[token]
	if l.maxConnections > 0 {
		if connections >= l.maxConnections {
			return fmt.Errorf("max connections reached: %d", l.maxConnections)
		}
	}

	l.connections[token] += amount
	l.totalConnections += int64(amount)
	return nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.next.ServeHTTP(w, r)

	if h.limiter != nil {
		token, amount, err := h.limiter.extract.Extract(r)
		if err != nil {
			log.Errorf("failed to extract source of the connection: %v", err)
			return
		}
		if err := h.limiter.acquire(token, amount); err != nil {
			log.Infof("limiting request source %s: %v", token, err)
			return
		}
	}

	go func() {
		h.proxy.Director = func(req *http.Request) {
			req.URL.Scheme = h.cfg.Scheme
			req.URL.Host = h.cfg.Host
			req.URL.RawQuery = r.URL.RawQuery
		}
		h.proxy.ServeHTTP(w, r)
	}()
}

func New(scheme, host string, timeout, keepalive, tlshandshaketimeout time.Duration, connections int64, variable string) (*Config, error) {
	if scheme == "" || host == "" {
		return nil, fmt.Errorf("Scheme and host can't be empty.")
	}
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("Invalid scheme, only http[s] allowed.")
	}
	if timeout < 0 {
		return nil, fmt.Errorf("Timeout can't be less zero.")
	}
	if keepalive < 0 {
		return nil, fmt.Errorf("Keepalive can't be less zero.")
	}
	if tlshandshaketimeout < 0 {
		return nil, fmt.Errorf("TLSHandshakeTimeout can't be less zero.")
	}
	if connections < 0 {
		return nil, fmt.Errorf("Connections limit can't be less zero.")
	}
	if strings.HasPrefix(variable, "request.header.") {
		header := strings.TrimPrefix(variable, "request.header.")
		if len(header) == 0 {
			return nil, fmt.Errorf("Wrong header: %s", header)
		}
	}
	if variable != "client.ip" && variable != "request.host" {
		return nil, fmt.Errorf("Unsupported limiting variable: '%s'", variable)
	}
	return &Config{
		Scheme:              scheme,
		Host:                host,
		Timeout:             timeout,
		KeepAlive:           keepalive,
		TLSHandshakeTimeout: tlshandshaketimeout,
		Connections:         connections,
		Variable:            variable,
	}, nil
}

func (c *Config) NewHandler(next http.Handler) (http.Handler, error) {
	proxy := &httputil.ReverseProxy{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			Dial: (&net.Dialer{
				Timeout:   c.Timeout,
				KeepAlive: c.KeepAlive,
			}).Dial,
			TLSHandshakeTimeout: c.TLSHandshakeTimeout,
		},
	}

	handler := &Handler{next: next, cfg: *c, proxy: proxy}

	if c.Connections > 0 {
		extract, err := utils.NewExtractor("client.ip")
		if err != nil {
			return nil, err
		}
		handler.limiter = &Limiter{
			mutex:          &sync.Mutex{},
			extract:        extract,
			maxConnections: c.Connections,
			connections:    make(map[string]int64),
		}
	}

	return handler, nil
}

func (c *Config) String() string {
	return fmt.Sprintf("scheme=%s, host=%s, timeout=%d, keepalive=%d, "+
		"tlshandshaketimeout=%d, connections=%d, variable=%s",
		c.Scheme, c.Host, c.Timeout, c.KeepAlive, c.TLSHandshakeTimeout, c.Connections, c.Variable)
}

func FromOther(c Config) (plugin.Middleware, error) {
	return New(
		c.Scheme,
		c.Host,
		time.Duration(c.Timeout),
		time.Duration(c.KeepAlive),
		time.Duration(c.TLSHandshakeTimeout),
		c.Connections,
		c.Variable,
	)
}

func FromCli(c *cli.Context) (plugin.Middleware, error) {
	return New(
		c.String("scheme"),
		c.String("host"),
		c.Duration("timeout"),
		c.Duration("keepalive"),
		c.Duration("tlshandshaketimeout"),
		int64(c.Int("connections")),
		c.String("variable"),
	)
}

func CliFlags() []cli.Flag {
	return []cli.Flag{
		cli.StringFlag{Name: "scheme", Usage: "Scheme of endpoint, http[s]"},
		cli.StringFlag{Name: "host", Usage: "Host or host:port of endpoint"},
		cli.DurationFlag{Name: "timeout", Usage: "Transport timeout", Value: DefaultTimeoutDuration},
		cli.DurationFlag{Name: "keepalive", Usage: "Transport KeepAlive", Value: DefaultKeepAliveDuration},
		cli.DurationFlag{Name: "tlshandshaketimeout", Usage: "Transport TLSHandshakeTimeout", Value: DefaultTLSHandshakeTimeoutDuration},
		cli.IntFlag{Name: "connections", Usage: "Limit amount of connections allowed for mirroring per variable value"},
		cli.StringFlag{Name: "variable", Value: "client.ip", Usage: "Limit variable to rate against, e.g. client.ip, request.host or request.header.X-Header"},
	}
}
