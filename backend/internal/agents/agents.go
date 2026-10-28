// Package agents defines how the backend talks to model-backed roles. The
// production implementation is Codex app-server (package codex); tests use
// scripted fakes.
package agents

import (
	"context"
	"encoding/json"
	"errors"
)

type Role string

const (
	Planner        Role = "planner"
	PlanReviewer   Role = "plan-reviewer"
	Generator      Role = "generator"
	LearningMentor Role = "learning-mentor"
	WorkReviewer   Role = "work-reviewer"
)

// Mount is an extra host directory visible to the agent.
type Mount struct {
	HostPath, Target string
	ReadOnly         bool
}

// TurnRequest is one model turn in a session. An empty ThreadID starts a new
// thread; otherwise the thread stored in SessionDir is resumed.
type TurnRequest struct {
	Role         Role
	SessionDir   string // host directory owned by this session: CODEX_HOME and workspace
	ThreadID     string
	Model        string
	Instructions string // developer instructions for a new thread
	Input        string
	OutputSchema json.RawMessage // constrains the final message; nil for free text
	Mounts       []Mount
}

type TurnResult struct {
	ThreadID string
	Text     string // final agent message
}

type Agent interface {
	Turn(ctx context.Context, req TurnRequest) (TurnResult, error)
}

// ErrTimeout marks a turn that exceeded its deadline.
var ErrTimeout = errors.New("agent turn timed out")
