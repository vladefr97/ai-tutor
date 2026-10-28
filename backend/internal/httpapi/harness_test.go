package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vladefr97/tutor-app/backend/internal/auth"
	"github.com/vladefr97/tutor-app/backend/internal/httpapi"
	"github.com/vladefr97/tutor-app/backend/internal/testdb"
	"github.com/vladefr97/tutor-app/contracts"
)

type fakeMailer struct {
	mu    sync.Mutex
	codes map[string]string
}

func (m *fakeMailer) SendCode(_ context.Context, email, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.codes[email] = code
	return nil
}

func (m *fakeMailer) last(email string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.codes[strings.ToLower(email)]
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type harness struct {
	t      *testing.T
	srv    *httptest.Server
	mail   *fakeMailer
	clock  *fakeClock
	auth   *auth.Service
	router routers.Router
	db     *pgxpool.Pool
}

const (
	codeTTL    = 10 * time.Minute
	sessionTTL = 24 * time.Hour
	adminEmail = "teacher@example.com"
)

var (
	specOnce sync.Once
	spec     *openapi3.T
)

func newHarness(t *testing.T) *harness {
	t.Helper()
	specOnce.Do(func() {
		raw, _ := contracts.FS.ReadFile("openapi.yaml")
		var err error
		spec, err = openapi3.NewLoader().LoadFromData(raw)
		if err != nil {
			panic(err)
		}
		spec.Servers = openapi3.Servers{{URL: "http://tutor.test/api"}}
	})
	router, err := legacy.NewRouter(spec)
	if err != nil {
		t.Fatal(err)
	}
	pool := testdb.New(t)
	h := &harness{t: t, mail: &fakeMailer{codes: map[string]string{}},
		clock: &fakeClock{t: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}, router: router, db: pool}
	h.auth = &auth.Service{DB: pool, Mailer: h.mail, Now: h.clock.Now,
		CodeTTL: codeTTL, SessionTTL: sessionTTL, Pepper: "test-pepper"}
	if err := h.auth.EnsureTeacher(context.Background(), adminEmail); err != nil {
		t.Fatal(err)
	}
	h.srv = httptest.NewServer(httpapi.New(httpapi.Deps{DB: pool, Auth: h.auth}))
	t.Cleanup(h.srv.Close)
	return h
}

type client struct {
	h    *harness
	http *http.Client
}

func (h *harness) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{h: h, http: &http.Client{Jar: jar}}
}

// do sends a JSON request and checks both request and response against the OpenAPI contract.
func (c *client) do(method, path string, body any) (int, []byte) {
	t := c.h.t
	t.Helper()
	var reqBody []byte
	if body != nil {
		reqBody, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, c.h.srv.URL+path, bytes.NewReader(reqBody))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	c.h.checkContract(req, reqBody, resp, respBody)
	return resp.StatusCode, respBody
}

func (h *harness) checkContract(req *http.Request, reqBody []byte, resp *http.Response, respBody []byte) {
	t := h.t
	t.Helper()
	specReq := req.Clone(context.Background())
	specReq.URL.Scheme, specReq.URL.Host, specReq.Host = "http", "tutor.test", "tutor.test"
	specReq.Body = io.NopCloser(bytes.NewReader(reqBody))
	route, params, err := h.router.FindRoute(specReq)
	if err != nil {
		t.Fatalf("%s %s is not in openapi.yaml: %v", req.Method, req.URL.Path, err)
	}
	opts := &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, IncludeResponseStatus: true}
	in := &openapi3filter.RequestValidationInput{Request: specReq, PathParams: params, Route: route, Options: opts}
	// Invalid requests are allowed on purpose in some tests; only well-formed ones are checked.
	reqOK := openapi3filter.ValidateRequest(context.Background(), in) == nil
	if !reqOK && resp.StatusCode < 400 {
		t.Fatalf("server accepted a request that violates the contract: %s %s", req.Method, req.URL.Path)
	}
	out := &openapi3filter.ResponseValidationInput{RequestValidationInput: in, Status: resp.StatusCode,
		Header: resp.Header, Body: io.NopCloser(bytes.NewReader(respBody)), Options: opts}
	if err := openapi3filter.ValidateResponse(context.Background(), out); err != nil {
		t.Fatalf("response violates openapi.yaml: %s %s → %d: %v\n%s", req.Method, req.URL.Path, resp.StatusCode, err, respBody)
	}
}

// login runs the full code flow and returns a client holding the session cookie.
func (h *harness) login(email string) *client {
	h.t.Helper()
	c := h.client()
	if st, b := c.do("POST", "/api/auth/code", map[string]string{"email": email}); st != 204 {
		h.t.Fatalf("request code: %d %s", st, b)
	}
	if st, b := c.do("POST", "/api/auth/verify", map[string]string{"email": email, "code": h.mail.last(email)}); st != 200 {
		h.t.Fatalf("verify: %d %s", st, b)
	}
	return c
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return v
}
