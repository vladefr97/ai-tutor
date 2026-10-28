package learning_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vladefr97/tutor-app/backend/internal/agents"
	"github.com/vladefr97/tutor-app/backend/internal/agents/codex"
	"github.com/vladefr97/tutor-app/backend/internal/generation"
	"github.com/vladefr97/tutor-app/backend/internal/learning"
	"github.com/vladefr97/tutor-app/backend/internal/storage"
	"github.com/vladefr97/tutor-app/backend/internal/testdb"
	"github.com/vladefr97/tutor-app/contracts"
)

// Real Work reviewer on the PM case «План переезда офиса» from the pm-basics
// example: a complete answer must pass, an answer without risks must not.
// Run with make verify-cases-live.
func TestLiveCaseReview(t *testing.T) {
	if os.Getenv("LIVE_CODEX") == "" {
		t.Skip("set LIVE_CODEX=1 (needs Docker, network, OPENAI_API_KEY, TEST_DATABASE_URL, local S3)")
	}
	ctx := context.Background()
	pool := testdb.New(t)
	store, err := storage.New(ctx, "localhost:7070", "tutor", "tutor-secret", "live-cases", false)
	if err != nil {
		t.Fatal(err)
	}
	var learner, course, plan, version string
	pool.QueryRow(ctx, `insert into accounts (email) values ('live-case@example.com') returning id`).Scan(&learner)
	pool.QueryRow(ctx, `insert into courses (learner_id, direction, status) values ($1, 'pm', 'ready') returning id`, learner).Scan(&course)
	pool.QueryRow(ctx, `insert into plan_versions (course_id, number, plan, explanation, approved) values ($1, 1, '{}', '-', true) returning id`, course).Scan(&plan)
	pool.QueryRow(ctx, `insert into course_versions (course_id, plan_version_id, content_hash, status) values ($1, $2, 'example', 'ready') returning id`, course, plan).Scan(&version)
	pool.Exec(ctx, `update courses set content_version_id = $2 where id = $1`, course, version)
	for _, part := range []struct{ file, key string }{{"course.json", "public/course.json"}, {"private.json", "private/private.json"}} {
		data, _ := contracts.FS.ReadFile("examples/course/valid/pm-basics/" + part.file)
		if err := store.Put(ctx, "courses/"+course+"/versions/"+version+"/"+part.key, data); err != nil {
			t.Fatal(err)
		}
	}
	cfg := agents.Config{InstructionsDir: "../../../agent-config", SessionsDir: t.TempDir(),
		Models: map[agents.Role]string{agents.WorkReviewer: "gpt-6-astra"}}
	agent := &codex.Runner{Image: "tutor-codex:0.163.0", APIKey: os.Getenv("OPENAI_API_KEY"), TurnTimeout: 5 * time.Minute}
	svc := &learning.Service{DB: pool, Agent: agent, Config: cfg, ReviewTimeout: 5 * time.Minute,
		Versions: &generation.Service{DB: pool, Store: store}}

	strong := `Цель: к 1 декабря 20 сотрудников работают в новом офисе, рабочие места и интернет готовы, простой не больше одного рабочего дня.
Задачи: 1) неделя 1 — утвердить план рассадки и бюджет; 2) неделя 1–2 — заключить договор с перевозчиком; 3) неделя 2–3 — заказать интернет и проверить электрику в новом офисе; 4) неделя 3–4 — закупить недостающую мебель; 5) неделя 4–5 — упаковка, маркировка коробок по отделам; 6) выходные в конце недели 5 — переезд; 7) понедельник недели 6 — проверка рабочих мест и интернета, неделя 6 — резерв на доделки.
Риски: 1) провайдер не успеет подключить интернет — заказываем в первую неделю, резерв: мобильные роутеры; 2) перевозчик сорвёт дату — договор со штрафом и запасной перевозчик; 3) мебель задержится — заказ с запасом в две недели, временные столы из старого офиса.`
	weak := `Цель: переехать в новый офис. Задачи: найти перевозчика, упаковать вещи, перевезти, расставить мебель, подключить интернет.`
	for name, tc := range map[string]struct {
		text string
		want string
	}{"strong": {strong, "passed"}, "weak": {weak, "failed"}} {
		at, err := svc.Submit(ctx, course, "t-plan-case", learning.Answer{Text: &tc.text})
		if err != nil {
			t.Fatal(err)
		}
		svc.Wait()
		var status string
		var feedback *string
		pool.QueryRow(ctx, `select status, feedback from attempts where id = $1`, at.ID).Scan(&status, &feedback)
		t.Logf("%s answer → %s: %v", name, status, deref(feedback))
		if status != tc.want {
			t.Errorf("%s answer: want %s, got %s", name, tc.want, status)
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
