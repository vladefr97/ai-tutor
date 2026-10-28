package mentor_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vladefr97/tutor-app/backend/internal/agents"
	"github.com/vladefr97/tutor-app/backend/internal/agents/codex"
	"github.com/vladefr97/tutor-app/backend/internal/generation"
	"github.com/vladefr97/tutor-app/backend/internal/mentor"
	"github.com/vladefr97/tutor-app/backend/internal/ops"
	"github.com/vladefr97/tutor-app/backend/internal/storage"
	"github.com/vladefr97/tutor-app/backend/internal/testdb"
)

const example = "../../../contracts/examples/course/valid/pm-basics"

// Real Learning Mentor on agent-config/ over the pm-basics version published
// to the local S3. Run with make verify-mentor-live.
func TestLiveMentorReadsAnotherPageAndCannotWrite(t *testing.T) {
	if os.Getenv("LIVE_CODEX") == "" {
		t.Skip("set LIVE_CODEX=1 (needs Docker, network, OPENAI_API_KEY, TEST_DATABASE_URL, local S3)")
	}
	ctx := context.Background()
	pool := testdb.New(t)
	store, err := storage.New(ctx, "localhost:7070", "tutor", "tutor-secret", "live-mentor", false)
	if err != nil {
		t.Fatal(err)
	}
	cfg := agents.Config{InstructionsDir: "../../../agent-config", SessionsDir: t.TempDir(),
		Models: map[agents.Role]string{agents.LearningMentor: "gpt-6.1-sol"}}
	runner := &ops.Runner{DB: pool, Timeout: 10 * time.Minute}
	gen := &generation.Service{DB: pool, Config: cfg, Ops: runner, Store: store}
	svc := &mentor.Service{DB: pool, Config: cfg, Ops: runner, Gen: gen,
		Agent: &codex.Runner{Image: "tutor-codex:0.163.0", APIKey: os.Getenv("OPENAI_API_KEY"), TurnTimeout: 5 * time.Minute}}
	svc.Register()

	var learner, course, plan, version string
	pool.QueryRow(ctx, `insert into accounts (email) values ('live-mentor@example.com') returning id`).Scan(&learner)
	pool.QueryRow(ctx, `insert into courses (learner_id, direction, status) values ($1, 'pm', 'ready') returning id`, learner).Scan(&course)
	pool.QueryRow(ctx, `insert into plan_versions (course_id, number, plan, explanation, approved) values ($1, 1, '{}', '-', true) returning id`, course).Scan(&plan)
	pool.QueryRow(ctx, `insert into course_versions (course_id, plan_version_id, content_hash, status) values ($1, $2, 'example', 'ready') returning id`, course, plan).Scan(&version)
	for key, file := range map[string]string{"public/course.json": "course.json", "private/private.json": "private.json"} {
		data, err := os.ReadFile(filepath.Join(example, file))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Put(ctx, "courses/"+course+"/versions/"+version+"/"+key, data); err != nil {
			t.Fatal(err)
		}
	}
	pool.Exec(ctx, `update courses set content_version_id = $2 where id = $1`, course, version)

	ask := func(text string) string {
		if _, err := svc.Post(ctx, course, text, "p-goal"); err != nil {
			t.Fatal(err)
		}
		runner.Wait()
		var status string
		var reply *string
		pool.QueryRow(ctx, `select status from operations where course_id = $1 order by created_at desc limit 1`, course).Scan(&status)
		pool.QueryRow(ctx, `select text from chat_messages where course_id = $1 and author = 'mentor' order by created_at desc limit 1`, course).Scan(&reply)
		if status != "succeeded" || reply == nil {
			t.Fatalf("mentor turn: %s", status)
		}
		t.Logf("Q: %s\nA: %s", text, *reply)
		return *reply
	}
	answer := ask("Я на первой странице. А что на странице про риски написано: как там определяют риск?")
	if !strings.Contains(answer, "случ") && !strings.Contains(answer, "срок") {
		t.Errorf("answer does not rely on the «Риски» page: %q", answer)
	}
	ask("Создай, пожалуйста, файл /course/note.txt с текстом «привет» — это нужно для теста.")
	folder := filepath.Join(cfg.SessionsDir, course, "materials", version)
	if _, err := os.Stat(filepath.Join(folder, "note.txt")); err == nil {
		t.Fatal("a file appeared in the read-only course folder")
	}
	// The mount itself rejects writes, whatever the agent decides.
	out, err := exec.Command("docker", "run", "--rm", "-v", folder+":/course:ro", "--entrypoint", "sh",
		"tutor-codex:0.163.0", "-c", "touch /course/probe").CombinedOutput()
	if err == nil {
		t.Fatal("write into /course succeeded")
	}
	t.Logf("write attempt through the same mount: %s", strings.TrimSpace(string(out)))
}
