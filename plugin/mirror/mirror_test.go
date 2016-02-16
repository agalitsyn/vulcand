package mirror

import (
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/codegangsta/cli"
	"github.com/vulcand/oxy/testutils"
	"github.com/vulcand/vulcand/plugin"
	. "gopkg.in/check.v1"

	"testing"
)

func TestCL(t *testing.T) { TestingT(t) }

type MirrorSuite struct {
}

var _ = Suite(&MirrorSuite{})

func (s *MirrorSuite) TestSpecIsOK(c *C) {
	c.Assert(plugin.NewRegistry().AddSpec(GetSpec()), IsNil)
}

func (s *MirrorSuite) TestNew(c *C) {
	m, err := New("http", "127.0.0.1:5000", 0, 0, 0, int64(0), "client.ip")
	c.Assert(m, NotNil)
	c.Assert(err, IsNil)

	c.Assert(m.String(), Not(Equals), "")

	out, err := m.NewHandler(nil)
	c.Assert(out, NotNil)
	c.Assert(err, IsNil)
}

func (s *MirrorSuite) TestNewBadParams(c *C) {
	// Empty scheme
	_, err := New("", "127.0.0.1", 0, 0, 0, int64(0), "client.ip")
	c.Assert(err, NotNil)

	// Invalid scheme
	_, err = New("blah", "127.0.0.1", 0, 0, 0, int64(0), "client.ip")
	c.Assert(err, NotNil)

	// Empty host
	_, err = New("http", "", 0, 0, 0, int64(1), "client.ip")
	c.Assert(err, NotNil)

	// Invalid Timeout
	_, err = New("http", "127.0.0.1", -1, 0, 0, int64(0), "client.ip")
	c.Assert(err, NotNil)

	// Invalid Keepalive
	_, err = New("http", "127.0.0.1", 0, -1, 0, int64(0), "client.ip")
	c.Assert(err, NotNil)

	// Invalid TLSHandshakeTimeout
	_, err = New("http", "127.0.0.1", 0, 0, -1, int64(0), "client.ip")
	c.Assert(err, NotNil)

	// Invalid Connections
	_, err = New("http", "127.0.0.1", 0, 0, 0, int64(-1), "client.ip")
	c.Assert(err, NotNil)

	// Invalid Variable
	_, err = New("http", "127.0.0.1", 0, 0, 0, int64(0), "dummy")
	c.Assert(err, NotNil)
}

func (s *MirrorSuite) TestFromOther(c *C) {
	m, err := New("http", "127.0.0.1", 0, 0, 0, int64(1), "client.ip")
	c.Assert(m, NotNil)
	c.Assert(err, IsNil)

	out, err := FromOther(*m)
	c.Assert(err, IsNil)
	c.Assert(out, DeepEquals, m)
}

func (s *MirrorSuite) TestAuthFromCli(c *C) {
	app := cli.NewApp()
	app.Name = "test"
	executed := false
	app.Action = func(ctx *cli.Context) {
		executed = true
		out, err := FromCli(ctx)
		c.Assert(out, NotNil)
		c.Assert(err, IsNil)

		m := out.(*Config)
		c.Assert(m.Scheme, Equals, "http")
		c.Assert(m.Host, Equals, "127.0.0.1:5000")
		c.Assert(m.Timeout, Equals, DefaultTimeoutDuration)
		c.Assert(m.KeepAlive, Equals, DefaultKeepAliveDuration)
		c.Assert(m.TLSHandshakeTimeout, Equals, DefaultTLSHandshakeTimeoutDuration)
		c.Assert(m.Connections, Equals, int64(0))
		c.Assert(m.Variable, Equals, "client.ip")
	}

	app.Flags = CliFlags()
	app.Run([]string{"test", "--scheme=http", "--host=127.0.0.1:5000"})
	c.Assert(executed, Equals, true)
}

func (s *MirrorSuite) TestRequestSuccess(c *C) {
	reciever := make(chan *http.Request, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("success"))
		reciever <- r
	})

	srv := httptest.NewServer(h)
	defer srv.Close()

	middleware, err := New("http", srv.Listener.Addr().String(), 0, 0, 0, int64(0), "client.ip")
	c.Assert(middleware, NotNil)
	c.Assert(err, IsNil)

	h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("success"))
	})

	hm, err := middleware.NewHandler(h)
	c.Assert(err, IsNil)

	src := httptest.NewServer(hm)
	defer src.Close()

	_, body, err := testutils.Get(src.URL)
	c.Assert(err, IsNil)
	c.Assert(string(body), Equals, "success")

	select {
	case req := <-reciever:
		c.Assert(req.Method, Equals, "GET")
		c.Assert(req.URL.Path, Equals, "/")
		c.Assert(req.Header.Get("Content-Type"), Equals, "")
		c.Assert(req.Header.Get("X-Forwarded-For"), Equals, "127.0.0.1")
	case <-time.After(time.Second):
		c.Error("timeout waiting for side effect to kick off")
	}
}

func (s *MirrorSuite) TestRequestLimitSuccess(c *C) {
	reciever := make(chan *http.Request, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("success"))
		reciever <- r
		close(reciever)
	})

	srv := httptest.NewServer(h)
	defer srv.Close()

	middleware, err := New("http", srv.Listener.Addr().String(), 0, 0, 0, int64(1), "client.ip")
	c.Assert(middleware, NotNil)
	c.Assert(err, IsNil)

	h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("success"))
	})

	hm, err := middleware.NewHandler(h)
	c.Assert(err, IsNil)

	src := httptest.NewServer(hm)
	defer src.Close()

	_, body, err := testutils.Get(src.URL)
	c.Assert(err, IsNil)
	c.Assert(string(body), Equals, "success")

	_, body, err = testutils.Get(src.URL)
	c.Assert(err, IsNil)
	c.Assert(string(body), Equals, "success")

	c.Assert(<-reciever, NotNil)
	c.Assert(<-reciever, IsNil)
}
