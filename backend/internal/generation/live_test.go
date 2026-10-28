package generation_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vladefr97/tutor-app/backend/internal/agents"
	"github.com/vladefr97/tutor-app/backend/internal/agents/codex"
	"github.com/vladefr97/tutor-app/backend/internal/generation"
	"github.com/vladefr97/tutor-app/backend/internal/ops"
	"github.com/vladefr97/tutor-app/backend/internal/planning"
	"github.com/vladefr97/tutor-app/backend/internal/storage"
	"github.com/vladefr97/tutor-app/backend/internal/testdb"
)

// Real chain for Project Management: skipped interview → approved plan →
// confirmation → generation, structure check, content review, upload.
// Run with make verify-generation-live. Keeps the generated files in LIVE_OUT_DIR if set.
func TestLiveProjectManagementCourse(t *testing.T) {
	if os.Getenv("LIVE_CODEX") == "" {
		t.Skip("set LIVE_CODEX=1 (needs Docker, network, OPENAI_API_KEY, TEST_DATABASE_URL, local S3)")
	}
	ctx := context.Background()
	pool := testdb.New(t)
	store, err := storage.New(ctx, "localhost:7070", "tutor", "tutor-secret", "live-generation", false)
	if err != nil {
		t.Fatal(err)
	}
	sessions := os.Getenv("LIVE_OUT_DIR")
	if sessions == "" {
		sessions = t.TempDir()
	}
	cfg := agents.Config{InstructionsDir: "../../../agent-config", SessionsDir: sessions, Models: map[agents.Role]string{
		agents.Planner: "gpt-6-astra", agents.PlanReviewer: "gpt-6-astra", agents.Generator: "gpt-6.1-sol", agents.WorkReviewer: "gpt-6-astra"}}
	agent := &codex.Runner{Image: "tutor-codex:0.163.0", APIKey: os.Getenv("OPENAI_API_KEY"), TurnTimeout: 30 * time.Minute}
	runner := &ops.Runner{DB: pool, Timeout: 90 * time.Minute}
	plan := &planning.Service{DB: pool, Agent: agent, Config: cfg, Ops: runner}
	plan.Register()
	gen := &generation.Service{DB: pool, Agent: agent, Config: cfg, Ops: runner, Store: store, MaxStructureFixes: 3}
	gen.Register()

	var learner, course string
	pool.QueryRow(ctx, `insert into accounts (email) values ('live-gen@example.com') returning id`).Scan(&learner)
	pool.QueryRow(ctx, `insert into courses (learner_id, direction) values ($1, 'pm') returning id`, learner).Scan(&course)
	if _, err := plan.SkipInterview(ctx, course); err != nil {
		t.Fatal(err)
	}
	runner.Wait()
	var versionID string
	if err := pool.QueryRow(ctx, `select id from plan_versions where course_id = $1 and approved order by number desc limit 1`, course).Scan(&versionID); err != nil {
		t.Fatalf("no approved plan: %v", err)
	}
	if err := plan.Confirm(ctx, course, versionID); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	runner.Wait()
	var status string
	var content *string
	pool.QueryRow(ctx, `select status, content_version_id from courses where id = $1`, course).Scan(&status, &content)
	rows, _ := pool.Query(ctx, `select kind, status, coalesce(error_message, ''), created_at, updated_at from operations where course_id = $1 order by created_at`, course)
	for rows.Next() {
		var kind, st, msg string
		var c, u time.Time
		rows.Scan(&kind, &st, &msg, &c, &u)
		t.Logf("operation %s %s %s (%s)", kind, st, msg, u.Sub(c).Round(time.Second))
	}
	rows, _ = pool.Query(ctx, `select role, dir from agent_sessions where course_id = $1 order by created_at`, course)
	for rows.Next() {
		var role, dir string
		rows.Scan(&role, &dir)
		t.Logf("session %s %s", role, filepath.Base(dir))
	}
	t.Logf("course %s status %s after %s", course, status, time.Since(start).Round(time.Second))
	if status != "ready" || content == nil {
		t.Fatalf("course is not ready: %s", status)
	}
	raw, err := gen.PublicCourse(ctx, course, *content)
	if err != nil {
		t.Fatal(err)
	}
	var pub struct {
		Title   string
		Modules []struct {
			Pages []struct{ Blocks []json.RawMessage }
		}
		Practice []json.RawMessage
	}
	json.Unmarshal(raw, &pub)
	pages, blocks := 0, 0
	for _, m := range pub.Modules {
		pages += len(m.Pages)
		for _, p := range m.Pages {
			blocks += len(p.Blocks)
		}
	}
	t.Logf("published %q: %d modules, %d pages, %d blocks, %d practice tasks", pub.Title, len(pub.Modules), pages, blocks, len(pub.Practice))
}
