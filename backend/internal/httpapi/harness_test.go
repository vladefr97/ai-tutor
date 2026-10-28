package httpapi_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vladefr97/tutor-app/backend/internal/agents"
	"github.com/vladefr97/tutor-app/backend/internal/auth"
	"github.com/vladefr97/tutor-app/backend/internal/generation"
	"github.com/vladefr97/tutor-app/backend/internal/httpapi"
	"github.com/vladefr97/tutor-app/backend/internal/ops"
	"github.com/vladefr97/tutor-app/backend/internal/planning"
	"github.com/vladefr97/tutor-app/backend/internal/storage"
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
	agent  *scriptedAgent
	ops    *ops.Runner
	deps   httpapi.Deps
	store  *flakyStore
	gen    *generation.Service
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
	h.agent = &scriptedAgent{}
	h.ops = &ops.Runner{DB: pool, Timeout: time.Minute}
	cfg := agents.Config{InstructionsDir: instructionsDir(t), SessionsDir: t.TempDir(),
		Models: map[agents.Role]string{agents.Planner: "planner-model", agents.PlanReviewer: "reviewer-model",
			agents.Generator: "generator-model", agents.WorkReviewer: "work-reviewer-model"}}
	plan := &planning.Service{DB: pool, Agent: h.agent, Config: cfg, Ops: h.ops}
	plan.Register()
	h.store = &flakyStore{Store: testStore(t)}
	h.gen = &generation.Service{DB: pool, Agent: h.agent, Config: cfg, Ops: h.ops, Store: h.store, MaxStructureFixes: 2}
	h.gen.Register()
	h.deps = httpapi.Deps{DB: pool, Auth: h.auth, Ops: h.ops, Planning: plan, Generation: h.gen}
	h.srv = httptest.NewServer(httpapi.New(h.deps))
	t.Cleanup(func() { h.ops.Wait(); h.srv.Close() })
	return h
}

// restart simulates a new backend process over the same database.
func (h *harness) restart() {
	h.ops.Wait()
	h.srv.Close()
	if err := h.ops.FailInterrupted(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	h.srv = httptest.NewServer(httpapi.New(h.deps))
}

func instructionsDir(t *testing.T) string {
	dir := t.TempDir()
	for path, text := range map[string]string{
		"roles/planner.md":          "ПРОМПТ ПЛАНИРОВЩИКА",
		"roles/generator.md":        "ПРОМПТ ГЕНЕРАТОРА",
		"roles/work-reviewer.md":    "ПРОМПТ РЕВЬЮЕРА РАБОТ",
		"roles/plan-reviewer.md":    "ПРОМПТ РЕВЬЮЕРА ПЛАНА",
		"directions/git/base.md":    "ОСНОВА GIT",
		"directions/pm/base.md":     "ОСНОВА PM",
		"directions/python/base.md": "ОСНОВА PYTHON",
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755)
		os.WriteFile(filepath.Join(dir, path), []byte(text), 0o644)
	}
	return dir
}

// testStore is a fresh bucket in the local S3 (make test-* starts it).
func testStore(t *testing.T) *storage.Store {
	t.Helper()
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		endpoint = "localhost:7070"
	}
	b := make([]byte, 4)
	rand.Read(b)
	st, err := storage.New(context.Background(), endpoint, "tutor", "tutor-secret", "test-"+hex.EncodeToString(b), false)
	if err != nil {
		t.Fatalf("S3 at %s: %v (run make db-up)", endpoint, err)
	}
	return st
}

// flakyStore fails puts after failPuts successful ones, if failPuts > 0.
type flakyStore struct {
	*storage.Store
	mu       sync.Mutex
	failPuts int
	puts     int
}

func (f *flakyStore) Put(ctx context.Context, key string, data []byte) error {
	f.mu.Lock()
	f.puts++
	fail := f.failPuts > 0 && f.puts > f.failPuts
	f.mu.Unlock()
	if fail {
		return errors.New("storage unavailable")
	}
	return f.Store.Put(ctx, key, data)
}

// scriptedAgent answers turns with a function set by the test and records requests.
type scriptedAgent struct {
	mu       sync.Mutex
	requests []agents.TurnRequest
	reply    func(req agents.TurnRequest) (agents.TurnResult, error)
}

func (a *scriptedAgent) Turn(ctx context.Context, req agents.TurnRequest) (agents.TurnResult, error) {
	a.mu.Lock()
	a.requests = append(a.requests, req)
	reply := a.reply
	a.mu.Unlock()
	if reply == nil {
		return agents.TurnResult{}, errors.New("no scripted reply")
	}
	return reply(req)
}

func (a *scriptedAgent) set(reply func(req agents.TurnRequest) (agents.TurnResult, error)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reply = reply
}

func (a *scriptedAgent) all() []agents.TurnRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]agents.TurnRequest(nil), a.requests...)
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
