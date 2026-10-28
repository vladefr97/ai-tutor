package planning_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/vladefr97/tutor-app/backend/internal/agents"
	"github.com/vladefr97/tutor-app/backend/internal/agents/codex"
	"github.com/vladefr97/tutor-app/backend/internal/ops"
	"github.com/vladefr97/tutor-app/backend/internal/planning"
	"github.com/vladefr97/tutor-app/backend/internal/testdb"
)

// Real Planner and Plan reviewer on the prompts in agent-config/. Run with make verify-planning-live.
func TestLivePlanningAfterSkippedInterview(t *testing.T) {
	if os.Getenv("LIVE_CODEX") == "" {
		t.Skip("set LIVE_CODEX=1 (needs Docker, network, OPENAI_API_KEY, TEST_DATABASE_URL)")
	}
	pool := testdb.New(t)
	ctx := context.Background()
	runner := &ops.Runner{DB: pool, Timeout: 20 * time.Minute}
	svc := &planning.Service{DB: pool, Ops: runner,
		Agent: &codex.Runner{Image: "tutor-codex:0.163.0", APIKey: os.Getenv("OPENAI_API_KEY"), TurnTimeout: 5 * time.Minute},
		Config: agents.Config{InstructionsDir: "../../../agent-config", SessionsDir: t.TempDir(),
			Models: map[agents.Role]string{agents.Planner: agents.LiveTestModel(), agents.PlanReviewer: agents.LiveTestModel()}}}
	svc.Register()
	var learner, course string
	pool.QueryRow(ctx, `insert into accounts (email) values ('live@example.com') returning id`).Scan(&learner)
	pool.QueryRow(ctx, `insert into courses (learner_id, direction) values ($1, 'pm') returning id`, learner).Scan(&course)

	if _, err := svc.SkipInterview(ctx, course); err != nil {
		t.Fatal(err)
	}
	runner.Wait()
	var status, opStatus string
	var opErr *string
	pool.QueryRow(ctx, `select status from courses where id = $1`, course).Scan(&status)
	pool.QueryRow(ctx, `select status, error_message from operations where course_id = $1 order by created_at desc limit 1`, course).Scan(&opStatus, &opErr)
	rows, _ := pool.Query(ctx, `select number, plan, review, approved from plan_versions where course_id = $1 order by number`, course)
	for rows.Next() {
		var n int
		var plan, review json.RawMessage
		var approved bool
		rows.Scan(&n, &plan, &review, &approved)
		t.Logf("version %d approved=%v\nplan: %s\nreview: %s", n, approved, plan, review)
	}
	t.Logf("course status %s, last operation %s %v", status, opStatus, opErr)
	if opStatus != "succeeded" || status != "plan_ready" {
		t.Fatalf("planning did not reach an approved plan: course %s, operation %s", status, opStatus)
	}
}
