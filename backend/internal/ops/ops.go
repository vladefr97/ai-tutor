// Package ops runs background operations of a course (agent turns,
// generation). Failures are recorded and shown; nothing is retried
// automatically — a retry is an explicit new operation with the same input.
package ops

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var (
	ErrBusy     = errors.New("course already has an active operation")
	ErrNotFound = errors.New("operation not found")
	ErrNotRetry = errors.New("only failed operations can be retried")
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Op struct {
	ID       string          `json:"id"`
	CourseID string          `json:"-"`
	Kind     string          `json:"kind"`
	Status   string          `json:"status"`
	Stage    *string         `json:"stage"`
	Input    json.RawMessage `json:"-"`
	Error    *Error          `json:"error"`
	// parent is the span of the request that started the operation in this
	// process; the operation span continues that trace.
	parent trace.SpanContext
}

func (o Op) Active() bool { return o.Status == "queued" || o.Status == "running" }

// Failure is returned by handlers to show a learner-facing message; any other
// error is shown as a generic failure and logged.
type Failure struct {
	Code, Message string
	Err           error
}

func (f *Failure) Error() string { return f.Code + ": " + f.Message + ": " + errString(f.Err) }
func (f *Failure) Unwrap() error { return f.Err }

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Next is an operation to start right after the current one succeeds.
type Next struct {
	Kind  string
	Input any
}

type Handler func(ctx context.Context, op Op) (*Next, error)

type Runner struct {
	DB       *pgxpool.Pool
	Timeout  time.Duration
	handlers map[string]Handler
	wg       sync.WaitGroup
}

func (r *Runner) Handle(kind string, h Handler) {
	if r.handlers == nil {
		r.handlers = map[string]Handler{}
	}
	r.handlers[kind] = h
}

const cols = `id, course_id, kind, status, stage, input, error_code, error_message`

func scan(row pgx.Row) (Op, error) {
	var o Op
	var code, msg *string
	err := row.Scan(&o.ID, &o.CourseID, &o.Kind, &o.Status, &o.Stage, &o.Input, &code, &msg)
	if code != nil {
		o.Error = &Error{Code: *code, Message: *msg}
	}
	return o, err
}

// Start records a queued operation and runs it in the background.
func (r *Runner) Start(ctx context.Context, tx pgx.Tx, courseID, kind string, input any) (Op, error) {
	in, err := json.Marshal(input)
	if err != nil {
		return Op{}, err
	}
	q := `insert into operations (course_id, kind, input) values ($1, $2, $3) returning ` + cols
	var row pgx.Row
	if tx != nil {
		row = tx.QueryRow(ctx, q, courseID, kind, in)
	} else {
		row = r.DB.QueryRow(ctx, q, courseID, kind, in)
	}
	op, err := scan(row)
	op.parent = trace.SpanContextFromContext(ctx)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Op{}, ErrBusy
	}
	if err != nil {
		return Op{}, err
	}
	if tx == nil {
		r.Launch(op)
	}
	return op, nil
}

// Launch runs a queued operation; call it after the transaction that created it commits.
func (r *Runner) Launch(op Op) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.run(op)
	}()
}

func (r *Runner) run(op Op) {
	ctx, cancel := context.WithTimeout(trace.ContextWithSpanContext(context.Background(), op.parent), r.Timeout)
	defer cancel()
	ctx, span := otel.Tracer("tutor-backend").Start(ctx, "operation "+op.Kind,
		trace.WithAttributes(attribute.String("operation.id", op.ID), attribute.String("operation.kind", op.Kind)))
	defer span.End()
	if _, err := r.DB.Exec(ctx, `update operations set status = 'running', updated_at = now() where id = $1`, op.ID); err != nil {
		slog.ErrorContext(ctx, "operation start", "op", op.ID, "err", err)
		return
	}
	op.Status = "running"
	h, ok := r.handlers[op.Kind]
	var err error
	var next *Next
	if !ok {
		err = errors.New("no handler for " + op.Kind)
	} else {
		next, err = h(ctx, op)
	}
	if err == nil {
		// The next operation is created in the same transaction, so the course
		// never looks idle between the two.
		var nextOp *Op
		err = pgx.BeginFunc(context.Background(), r.DB, func(tx pgx.Tx) error {
			if _, err := tx.Exec(context.Background(), `update operations set status = 'succeeded', updated_at = now() where id = $1`, op.ID); err != nil {
				return err
			}
			if next == nil {
				return nil
			}
			o, err := r.Start(trace.ContextWithSpanContext(context.Background(), trace.SpanContextFromContext(ctx)), tx, op.CourseID, next.Kind, next.Input)
			nextOp = &o
			return err
		})
		if err != nil {
			slog.ErrorContext(ctx, "operation finish", "op", op.ID, "err", err)
			return
		}
		slog.InfoContext(ctx, "operation succeeded", "op", op.ID, "kind", op.Kind)
		if nextOp != nil {
			r.Launch(*nextOp)
		}
		return
	}
	f := &Failure{Code: "operation_failed", Message: "Не получилось. Попробуйте ещё раз."}
	errors.As(err, &f)
	span.SetStatus(codes.Error, f.Code)
	slog.ErrorContext(ctx, "operation failed", "op", op.ID, "kind", op.Kind, "err", err)
	if _, err := r.DB.Exec(context.Background(), `update operations set status = 'failed', error_code = $2,
		error_message = $3, updated_at = now() where id = $1`, op.ID, f.Code, f.Message); err != nil {
		slog.ErrorContext(ctx, "operation fail", "op", op.ID, "err", err)
	}
}

func (r *Runner) SetStage(ctx context.Context, opID, stage string) error {
	_, err := r.DB.Exec(ctx, `update operations set stage = $2, updated_at = now() where id = $1`, opID, stage)
	return err
}

// Get returns an operation of a course owned by learnerID.
func (r *Runner) Get(ctx context.Context, opID, learnerID string) (Op, error) {
	op, err := scan(r.DB.QueryRow(ctx, `select `+prefixed("o")+` from operations o join courses c on c.id = o.course_id
		where o.id::text = $1 and c.learner_id = $2`, opID, learnerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return op, ErrNotFound
	}
	return op, err
}

// Current is the course's latest operation unless it succeeded.
func (r *Runner) Current(ctx context.Context, courseID string) (*Op, error) {
	op, err := scan(r.DB.QueryRow(ctx, `select `+cols+` from operations where course_id = $1 order by created_at desc limit 1`, courseID))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && op.Status == "succeeded") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &op, nil
}

// Retry starts a new operation with the same kind and input as a failed one.
func (r *Runner) Retry(ctx context.Context, opID, learnerID string) (Op, error) {
	op, err := r.Get(ctx, opID, learnerID)
	if err != nil {
		return op, err
	}
	if op.Status != "failed" {
		return op, ErrNotRetry
	}
	return r.Start(ctx, nil, op.CourseID, op.Kind, op.Input)
}

// FailInterrupted marks operations left active by a previous process as failed.
func (r *Runner) FailInterrupted(ctx context.Context) error {
	_, err := r.DB.Exec(ctx, `update operations set status = 'failed', error_code = 'interrupted',
		error_message = 'Операция прервана перезапуском сервиса. Нажмите «Повторить».', updated_at = now()
		where status in ('queued', 'running')`)
	return err
}

// Wait blocks until all launched operations finish (tests, shutdown).
func (r *Runner) Wait() { r.wg.Wait() }

func prefixed(alias string) string {
	return alias + ".id, " + alias + ".course_id, " + alias + ".kind, " + alias + ".status, " + alias + ".stage, " +
		alias + ".input, " + alias + ".error_code, " + alias + ".error_message"
}
