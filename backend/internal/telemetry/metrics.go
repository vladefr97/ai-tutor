// Package telemetry wires Prometheus metrics, OpenTelemetry traces and
// structured logs. Metric labels never carry learner or course IDs: routes are
// ServeMux patterns, not URLs.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/vladefr97/tutor-app/backend/internal/agents"
	"github.com/vladefr97/tutor-app/backend/internal/visits"
)

// Tracer is the backend's OpenTelemetry tracer (resolved lazily, after Setup).
func Tracer() trace.Tracer { return otel.Tracer("tutor-backend") }

var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total", Help: "HTTP requests by method, route pattern and status code.",
	}, []string{"method", "route", "code"})
	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "http_request_duration_seconds", Help: "HTTP request duration; the code label separates fast errors from successes.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}, []string{"method", "route", "code"})
	httpInFlight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "http_requests_in_flight", Help: "HTTP requests being served.",
	})
	externalCalls = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "external_calls_total", Help: "Calls to external services (model, GitHub) by outcome ok|error|timeout.",
	}, []string{"service", "operation", "outcome"})
	externalDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "external_call_duration_seconds", Help: "External call duration.",
		Buckets: []float64{.1, .5, 1, 2.5, 5, 10, 30, 60, 120, 300},
	}, []string{"service", "operation", "outcome"})
	dbQueries = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "db_query_duration_seconds", Help: "PostgreSQL query duration by outcome ok|error.",
		Buckets: []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1},
	}, []string{"outcome"})
)

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Middleware records HTTP metrics and a span per request and returns the trace
// ID in X-Trace-Id. It must wrap the ServeMux: the mux sets r.Pattern on the
// same request value, which becomes the route label after serving.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		httpInFlight.Inc()
		defer httpInFlight.Dec()
		ctx, span := Tracer().Start(r.Context(), r.Method, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		w.Header().Set("X-Trace-Id", span.SpanContext().TraceID().String())
		sw := &statusWriter{ResponseWriter: w}
		r = r.WithContext(ctx)
		next.ServeHTTP(sw, r)
		if sw.code == 0 {
			sw.code = http.StatusOK
		}
		route := Route(r)
		code := strconv.Itoa(sw.code)
		httpRequests.WithLabelValues(r.Method, route, code).Inc()
		httpDuration.WithLabelValues(r.Method, route, code).Observe(time.Since(start).Seconds())
		span.SetName(r.Method + " " + route)
		span.SetAttributes(attribute.String("http.request.method", r.Method), attribute.String("http.route", route),
			attribute.Int("http.response.status_code", sw.code))
		if sw.code >= 500 {
			span.SetStatus(codes.Error, http.StatusText(sw.code))
		}
		slog.InfoContext(ctx, "http request", "method", r.Method, "route", route, "status", sw.code,
			"duration_ms", time.Since(start).Milliseconds())
	})
}

// Route is the matched pattern without the method, or "unmatched".
func Route(r *http.Request) string {
	p := r.Pattern
	if i := strings.IndexByte(p, ' '); i >= 0 {
		p = p[i+1:]
	}
	if p == "" {
		return "unmatched"
	}
	return p
}

// External times a call to an external service and records its outcome as a
// metric and a child span of ctx. Deadline errors and agents.ErrTimeout count
// as timeouts. GitHub calls use service "github".
func External(ctx context.Context, service, operation string, call func(ctx context.Context) error) error {
	ctx, span := Tracer().Start(ctx, service+" "+operation, trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	start := time.Now()
	err := call(ctx)
	outcome := "ok"
	switch {
	case err == nil:
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, agents.ErrTimeout):
		outcome = "timeout"
	default:
		outcome = "error"
	}
	if err != nil {
		span.SetStatus(codes.Error, outcome)
		span.RecordError(err)
	}
	span.SetAttributes(attribute.String("outcome", outcome))
	externalCalls.WithLabelValues(service, operation, outcome).Inc()
	externalDuration.WithLabelValues(service, operation, outcome).Observe(time.Since(start).Seconds())
	return err
}

// Agent measures every model turn as an external call (service "model", operation = role).
type Agent struct{ Next agents.Agent }

func (a Agent) Turn(ctx context.Context, req agents.TurnRequest) (res agents.TurnResult, err error) {
	err = External(ctx, "model", string(req.Role), func(ctx context.Context) error {
		trace.SpanFromContext(ctx).SetAttributes(attribute.String("model", req.Model))
		var e error
		res, e = a.Next.Turn(ctx, req)
		return e
	})
	return res, err
}

// PGXTracer records query duration and errors (pgx.QueryTracer).
type PGXTracer struct{}

type queryStartKey struct{}

func (PGXTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, queryStartKey{}, time.Now())
}

func (PGXTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	start, ok := ctx.Value(queryStartKey{}).(time.Time)
	if !ok {
		return
	}
	outcome := "ok"
	if data.Err != nil && !errors.Is(data.Err, pgx.ErrNoRows) {
		outcome = "error"
	}
	dbQueries.WithLabelValues(outcome).Observe(time.Since(start).Seconds())
}

var (
	poolTotal      = prometheus.NewDesc("db_pool_connections_total", "Connections in the pool.", nil, nil)
	poolIdle       = prometheus.NewDesc("db_pool_connections_idle", "Idle connections.", nil, nil)
	poolAcquired   = prometheus.NewDesc("db_pool_connections_acquired", "Connections in use.", nil, nil)
	poolAcquires   = prometheus.NewDesc("db_pool_acquires_total", "Successful connection acquires.", nil, nil)
	poolAcquireDur = prometheus.NewDesc("db_pool_acquire_duration_seconds_total", "Total time spent acquiring connections.", nil, nil)
	poolEmpty      = prometheus.NewDesc("db_pool_empty_acquires_total", "Acquires that had to wait for a connection.", nil, nil)
	learnersTotal  = prometheus.NewDesc("tutor_learners_total", "Unique learners who ever visited (teachers excluded).", nil, nil)
	learnersActive = prometheus.NewDesc("tutor_learners_regularly_active", "Learners with visits on at least two Moscow calendar days in D-6…D.", nil, nil)
	attendanceOK   = prometheus.NewDesc("tutor_attendance_query_ok", "1 if the attendance query succeeded on this scrape.", nil, nil)
)

// PoolCollector exports pgxpool statistics.
type PoolCollector struct{ Pool *pgxpool.Pool }

func (PoolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{poolTotal, poolIdle, poolAcquired, poolAcquires, poolAcquireDur, poolEmpty} {
		ch <- d
	}
}

func (c PoolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.Pool.Stat()
	ch <- prometheus.MustNewConstMetric(poolTotal, prometheus.GaugeValue, float64(s.TotalConns()))
	ch <- prometheus.MustNewConstMetric(poolIdle, prometheus.GaugeValue, float64(s.IdleConns()))
	ch <- prometheus.MustNewConstMetric(poolAcquired, prometheus.GaugeValue, float64(s.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(poolAcquires, prometheus.CounterValue, float64(s.AcquireCount()))
	ch <- prometheus.MustNewConstMetric(poolAcquireDur, prometheus.CounterValue, s.AcquireDuration().Seconds())
	ch <- prometheus.MustNewConstMetric(poolEmpty, prometheus.CounterValue, float64(s.EmptyAcquireCount()))
}

// AttendanceCollector computes the two attendance figures on each scrape.
type AttendanceCollector struct {
	Pool *pgxpool.Pool
	Now  func() time.Time
}

func (AttendanceCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- learnersTotal
	ch <- learnersActive
	ch <- attendanceOK
}

func (c AttendanceCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, err := visits.Count(ctx, c.Pool, c.Now())
	if err != nil {
		ch <- prometheus.MustNewConstMetric(attendanceOK, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(attendanceOK, prometheus.GaugeValue, 1)
	ch <- prometheus.MustNewConstMetric(learnersTotal, prometheus.GaugeValue, float64(a.TotalLearners))
	ch <- prometheus.MustNewConstMetric(learnersActive, prometheus.GaugeValue, float64(a.RegularlyActive))
}

// TestRoutes adds routes that answer with a chosen status or after a delay, so
// verify-observability can produce known 5xx and slow requests. Enabled only
// with OBSERVABILITY_TEST_ROUTES=1; never in a normal run.
func TestRoutes(app http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/observability-test/status/{code}", func(w http.ResponseWriter, r *http.Request) {
		code, err := strconv.Atoi(r.PathValue("code"))
		if err != nil || code < 200 || code > 599 {
			code = http.StatusBadRequest
		}
		w.WriteHeader(code)
	})
	mux.HandleFunc("GET /api/observability-test/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/", app)
	return mux
}
