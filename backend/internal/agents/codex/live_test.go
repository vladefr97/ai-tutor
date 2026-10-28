package codex_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vladefr97/tutor-app/backend/internal/agents"
	"github.com/vladefr97/tutor-app/backend/internal/agents/codex"
)

// Real model call through a Codex container. Run with make verify-codex-live.
func TestLiveTurnAndResume(t *testing.T) {
	if os.Getenv("LIVE_CODEX") == "" {
		t.Skip("set LIVE_CODEX=1 (needs Docker, network, OPENAI_API_KEY)")
	}
	r := &codex.Runner{Image: "tutor-codex:0.163.0", APIKey: os.Getenv("OPENAI_API_KEY"), TurnTimeout: 3 * time.Minute}
	dir := t.TempDir()
	model := os.Getenv("MODEL_PLANNER")
	if model == "" {
		model = agents.LiveTestModel()
	}
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["word"],"properties":{"word":{"type":"string"}}}`)
	first, err := r.Turn(context.Background(), agents.TurnRequest{SessionDir: dir, Model: model,
		Instructions: "Отвечай кратко. Инструменты не используй.",
		Input:        "Запомни кодовое слово «пеликан». Верни его в поле word.", OutputSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Word string }
	if err := json.Unmarshal([]byte(first.Text), &out); err != nil || !strings.Contains(strings.ToLower(out.Word), "пеликан") {
		t.Fatalf("structured output: %q (%v)", first.Text, err)
	}
	if _, err := os.Stat(dir + "/home/auth.json"); !os.IsNotExist(err) {
		t.Fatal("API key left on disk after the turn")
	}
	second, err := r.Turn(context.Background(), agents.TurnRequest{SessionDir: dir, Model: model, ThreadID: first.ThreadID,
		Input: "Какое кодовое слово я просил запомнить? Ответь одним словом."})
	if err != nil {
		t.Fatal(err)
	}
	if second.ThreadID != first.ThreadID || !strings.Contains(strings.ToLower(second.Text), "пеликан") {
		t.Fatalf("resume lost the thread: %+v", second)
	}
	t.Logf("thread %s, model %s: %q", first.ThreadID, model, second.Text)
}
