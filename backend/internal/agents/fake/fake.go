// Package fake is a deterministic stand-in for model roles, used by browser
// scenarios (AGENT_FAKE=1). State lives in the session directory, so it
// survives backend restarts like a real thread. Never used for acceptance of
// model behaviour.
package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vladefr97/tutor-app/backend/internal/agents"
)

type Agent struct{}

func (Agent) Turn(_ context.Context, req agents.TurnRequest) (agents.TurnResult, error) {
	if err := os.MkdirAll(req.SessionDir, 0o777); err != nil {
		return agents.TurnResult{}, err
	}
	thread := req.ThreadID
	if thread == "" {
		thread = "fake-" + filepath.Base(filepath.Dir(req.SessionDir)) + "-" + filepath.Base(req.SessionDir)
	}
	switch req.Role {
	case agents.Planner:
		return planner(req, thread)
	}
	return agents.TurnResult{}, errors.New("fake agent: role " + string(req.Role) + " is not scripted")
}

// failOnce fails the first time a given learner text containing «сбой» is seen.
func failOnce(dir, text string) bool {
	if !strings.Contains(text, "сбой") {
		return false
	}
	h := sha256.Sum256([]byte(text))
	marker := filepath.Join(dir, "failed-"+hex.EncodeToString(h[:8]))
	if _, err := os.Stat(marker); err == nil {
		return false
	}
	os.WriteFile(marker, nil, 0o666)
	return true
}

func planner(req agents.TurnRequest, thread string) (agents.TurnResult, error) {
	if failOnce(req.SessionDir, req.Input) {
		return agents.TurnResult{ThreadID: thread}, errors.New("fake: scripted failure")
	}
	countFile := filepath.Join(req.SessionDir, "learner-turns")
	raw, _ := os.ReadFile(countFile)
	n, _ := strconv.Atoi(string(raw))
	turn := map[string]any{"kind": "interview_turn", "goal": nil, "readyForPlan": false,
		"profile": map[string]any{"preparation": nil, "context": nil, "preferences": nil}}
	if strings.HasPrefix(req.Input, "[Начало интервью]") {
		turn["message"] = "Привет! Я твой наставник. Какую цель ты хочешь достичь?"
	} else {
		n++
		os.WriteFile(countFile, []byte(strconv.Itoa(n)), 0o666)
		turn["goal"] = "Цель ученика: " + req.Input
		if n == 1 {
			turn["message"] = "Понял. Что ты уже умеешь?"
		} else {
			turn["message"] = "Спасибо, этого достаточно. Составляю план."
			turn["readyForPlan"] = true
			turn["profile"] = map[string]any{"preparation": req.Input, "context": nil, "preferences": nil}
		}
	}
	b, _ := json.Marshal(turn)
	return agents.TurnResult{ThreadID: thread, Text: string(b)}, nil
}
