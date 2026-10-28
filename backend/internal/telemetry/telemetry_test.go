package telemetry_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/vladefr97/tutor-app/backend/internal/agents"
	"github.com/vladefr97/tutor-app/backend/internal/telemetry"
	"github.com/vladefr97/tutor-app/backend/internal/testdb"
	"github.com/vladefr97/tutor-app/backend/internal/visits"
)

func counter(name string, labels ...string) float64 {
	mfs, _ := prometheus.DefaultGatherer.Gather()
	want := map[string]string{}
	for i := 0; i+1 < len(labels); i += 2 {
		want[labels[i]] = labels[i+1]
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	metrics:
		for _, m := range mf.GetMetric() {
			got := map[string]string{}
			for _, l := range m.GetLabel() {
				got[l.GetName()] = l.GetValue()
			}
			for k, v := range want {
				if got[k] != v {
					continue metrics
				}
			}
			if m.Counter != nil {
				return m.Counter.GetValue()
			}
			return float64(m.Histogram.GetSampleCount())
		}
	}
	return 0
}

func TestHTTPMetricsUseRoutePatternsAndCodes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/courses/{courseId}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("courseId") == "missing" {
			http.Error(w, "nope", http.StatusNotFound)
			return
		}
		w.Write([]byte("ok"))
	})
	srv := httptest.NewServer(telemetry.Middleware(mux))
	defer srv.Close()
	route := "/api/courses/{courseId}"
	ok0 := counter("http_requests_total", "method", "GET", "route", route, "code", "200")
	nf0 := counter("http_requests_total", "method", "GET", "route", route, "code", "404")
	um0 := counter("http_requests_total", "route", "unmatched", "code", "404")
	for _, p := range []string{"/api/courses/0b6c1d2e-learner-a", "/api/courses/7f00-learner-b", "/api/courses/missing", "/nowhere"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Header.Get("X-Trace-Id") == "" {
			t.Fatal("no X-Trace-Id")
		}
		resp.Body.Close()
	}
	if d := counter("http_requests_total", "method", "GET", "route", route, "code", "200") - ok0; d != 2 {
		t.Fatalf("2xx delta %v", d)
	}
	if d := counter("http_requests_total", "method", "GET", "route", route, "code", "404") - nf0; d != 1 {
		t.Fatalf("404 delta %v", d)
	}
	if d := counter("http_requests_total", "route", "unmatched", "code", "404") - um0; d != 1 {
		t.Fatalf("unmatched delta %v", d)
	}
	if c := counter("http_request_duration_seconds", "route", route, "code", "404"); c == 0 {
		t.Fatal("no duration sample for the error code")
	}
	mfs, _ := prometheus.DefaultGatherer.Gather()
	for _, mf := range mfs {
		if s := mf.String(); strings.Contains(s, "learner-a") || strings.Contains(s, "learner-b") {
			t.Fatalf("ID leaked into metric %s", mf.GetName())
		}
	}
}

func TestExternalCallOutcomes(t *testing.T) {
	ctx := context.Background()
	before := map[string]float64{}
	for _, o := range []string{"ok", "error", "timeout"} {
		before[o] = counter("external_calls_total", "service", "model", "operation", "planner", "outcome", o)
	}
	a := telemetry.Agent{Next: agentFunc(func(req agents.TurnRequest) error {
		switch req.Input {
		case "err":
			return errors.New("provider down")
		case "timeout":
			return fmt.Errorf("%w after 1s", agents.ErrTimeout)
		}
		return nil
	})}
	for _, in := range []string{"ok", "err", "timeout", "ok"} {
		a.Turn(ctx, agents.TurnRequest{Role: agents.Planner, Input: in})
	}
	for o, want := range map[string]float64{"ok": 2, "error": 1, "timeout": 1} {
		if d := counter("external_calls_total", "service", "model", "operation", "planner", "outcome", o) - before[o]; d != want {
			t.Fatalf("%s: delta %v, want %v", o, d, want)
		}
	}
}

type agentFunc func(agents.TurnRequest) error

func (f agentFunc) Turn(_ context.Context, req agents.TurnRequest) (agents.TurnResult, error) {
	return agents.TurnResult{}, f(req)
}

func TestLogsRedactSecrets(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(telemetry.NewHandler(slog.NewJSONHandler(&buf, nil)))
	log.With("token", "tok-123").Info("login", "code", "482913", "apiKey", "sk-secret",
		slog.Group("req", "Authorization", "Bearer abc", "cookie", "tutor_session=xyz"), "email", "a@example.com")
	out := buf.String()
	for _, secret := range []string{"tok-123", "482913", "sk-secret", "Bearer abc", "tutor_session=xyz"} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q in log: %s", secret, out)
		}
	}
	if !strings.Contains(out, "a@example.com") {
		t.Fatalf("non-secret attribute lost: %s", out)
	}
}

func TestAttendanceCollector(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for _, v := range []struct {
		email   string
		teacher bool
		days    []int
	}{{"a@x", false, []int{0, 1}}, {"b@x", false, []int{0}}, {"t@x", true, []int{0, 1}}} {
		var id string
		if err := pool.QueryRow(ctx, `insert into accounts (email, is_teacher) values ($1, $2) returning id`, v.email, v.teacher).Scan(&id); err != nil {
			t.Fatal(err)
		}
		for _, d := range v.days {
			if err := visits.Record(ctx, pool, id, now.AddDate(0, 0, -d)); err != nil {
				t.Fatal(err)
			}
		}
	}
	c := telemetry.AttendanceCollector{Pool: pool, Now: func() time.Time { return now }}
	want := `
# HELP tutor_learners_regularly_active Learners with visits on at least two Moscow calendar days in D-6…D.
# TYPE tutor_learners_regularly_active gauge
tutor_learners_regularly_active 1
# HELP tutor_learners_total Unique learners who ever visited (teachers excluded).
# TYPE tutor_learners_total gauge
tutor_learners_total 2
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), "tutor_learners_total", "tutor_learners_regularly_active"); err != nil {
		t.Fatal(err)
	}
}
