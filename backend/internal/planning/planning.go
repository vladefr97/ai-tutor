// Package planning runs the interview with the Planner mentor and, later,
// plan drafting and review. Each step is a background operation.
package planning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vladefr97/tutor-app/backend/internal/agents"
	"github.com/vladefr97/tutor-app/backend/internal/ops"
)

const KindMentorReply = "mentor_reply"

var ErrWrongState = errors.New("action is not allowed in the current course state")

type Profile struct {
	Preparation *string `json:"preparation"`
	Context     *string `json:"context"`
	Preferences *string `json:"preferences"`
}

var DirectionTitles = map[string]string{"python": "Python", "git": "Git", "pm": "управления проектами"}

// DefaultProfile is used when the interview is skipped.
func DefaultProfile() Profile {
	p, c, pref := "Начинает с нуля", "Не уточнялся", "На «вы», дружелюбно, простыми словами"
	return Profile{&p, &c, &pref}
}

type Service struct {
	DB     *pgxpool.Pool
	Agent  agents.Agent
	Config agents.Config
	Ops    *ops.Runner
}

func (s *Service) Register() {
	s.Ops.Handle(KindMentorReply, s.mentorReply)
}

type replyInput struct {
	Start bool   `json:"start,omitempty"`
	Text  string `json:"text,omitempty"`
}

// StartInterview queues the mentor's first message for a new course.
func (s *Service) StartInterview(ctx context.Context, courseID string) (ops.Op, error) {
	return s.Ops.Start(ctx, nil, courseID, KindMentorReply, replyInput{Start: true})
}

// LearnerMessage stores the learner's message and queues the mentor's reply.
func (s *Service) LearnerMessage(ctx context.Context, courseID, text string) (ops.Op, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return ops.Op{}, err
	}
	defer tx.Rollback(ctx)
	var status string
	if err := tx.QueryRow(ctx, `select status from courses where id = $1 for update`, courseID).Scan(&status); err != nil {
		return ops.Op{}, err
	}
	if status != "interview" {
		return ops.Op{}, ErrWrongState
	}
	op, err := s.Ops.Start(ctx, tx, courseID, KindMentorReply, replyInput{Text: text})
	if err != nil {
		return op, err
	}
	if _, err := tx.Exec(ctx, `insert into messages (course_id, author, text) values ($1, 'learner', $2)`, courseID, text); err != nil {
		return op, err
	}
	if err := tx.Commit(ctx); err != nil {
		return op, err
	}
	s.Ops.Launch(op)
	return op, nil
}

// SkipInterview keeps a goal the learner already named, otherwise sets the
// direction basics, fills a default profile and moves on to planning.
func (s *Service) SkipInterview(ctx context.Context, courseID string) error {
	def, _ := json.Marshal(DefaultProfile())
	tag, err := s.DB.Exec(ctx, `update courses set status = 'planning',
		goal = coalesce(goal, 'Основы ' || $2::text),
		profile = $3::jsonb || jsonb_strip_nulls(coalesce(profile, '{}'))
		where id = $1 and status = 'interview'
		and not exists (select 1 from operations where course_id = $1 and status in ('queued', 'running'))`,
		courseID, DirectionTitles[s.direction(ctx, courseID)], def)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrWrongState
	}
	return nil
}

func (s *Service) direction(ctx context.Context, courseID string) string {
	var d string
	s.DB.QueryRow(ctx, `select direction from courses where id = $1`, courseID).Scan(&d)
	return d
}

type session struct {
	ID, Dir, Model string
	ThreadID       *string
}

// plannerSession returns the course's Planner session, creating it on first use.
func (s *Service) plannerSession(ctx context.Context, courseID string) (session, error) {
	var ss session
	err := s.DB.QueryRow(ctx, `select id, dir, model, thread_id from agent_sessions
		where course_id = $1 and role = $2 order by created_at desc limit 1`, courseID, agents.Planner).
		Scan(&ss.ID, &ss.Dir, &ss.Model, &ss.ThreadID)
	if err == nil {
		return ss, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ss, err
	}
	ss.Dir = s.Config.SessionDir(courseID, agents.Planner, 1)
	ss.Model = s.Config.Models[agents.Planner]
	err = s.DB.QueryRow(ctx, `insert into agent_sessions (course_id, role, dir, model) values ($1, $2, $3, $4) returning id`,
		courseID, agents.Planner, ss.Dir, ss.Model).Scan(&ss.ID)
	return ss, err
}

type interviewTurn struct {
	Message      string  `json:"message"`
	Goal         *string `json:"goal"`
	Profile      Profile `json:"profile"`
	ReadyForPlan bool    `json:"readyForPlan"`
}

func agentFailure(err error) error {
	if errors.Is(err, agents.ErrTimeout) {
		return &ops.Failure{Code: "agent_timeout", Message: "Наставник не ответил вовремя. Нажмите «Повторить».", Err: err}
	}
	return &ops.Failure{Code: "agent_failed", Message: "Наставник не ответил. Нажмите «Повторить».", Err: err}
}

func (s *Service) mentorReply(ctx context.Context, op ops.Op) error {
	var in replyInput
	if err := json.Unmarshal(op.Input, &in); err != nil {
		return err
	}
	direction := s.direction(ctx, op.CourseID)
	ss, err := s.plannerSession(ctx, op.CourseID)
	if err != nil {
		return err
	}
	req := agents.TurnRequest{Role: agents.Planner, SessionDir: ss.Dir, Model: ss.Model,
		OutputSchema: agents.OutputSchema(agents.KindInterviewTurn), Input: in.Text}
	if in.Start {
		req.Input = fmt.Sprintf("[Начало интервью]\nНаправление: %s", direction)
	}
	if ss.ThreadID != nil {
		req.ThreadID = *ss.ThreadID
	} else if req.Instructions, err = s.Config.Instructions(agents.Planner, direction); err != nil {
		return err
	}
	res, err := s.Agent.Turn(ctx, req)
	if res.ThreadID != "" && ss.ThreadID == nil {
		if _, err := s.DB.Exec(ctx, `update agent_sessions set thread_id = $2 where id = $1`, ss.ID, res.ThreadID); err != nil {
			return err
		}
	}
	if err != nil {
		return agentFailure(err)
	}
	var turn interviewTurn
	if err := agents.ParseResult(agents.KindInterviewTurn, res.Text, &turn); err != nil {
		return agentFailure(err)
	}
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into messages (course_id, author, text) values ($1, 'mentor', $2)`, op.CourseID, turn.Message); err != nil {
			return err
		}
		profile, _ := json.Marshal(turn.Profile)
		next := "interview"
		if turn.ReadyForPlan {
			next = "planning"
		}
		_, err := tx.Exec(ctx, `update courses set goal = coalesce($2, goal), profile = $3, status = $4
			where id = $1 and status = 'interview'`, op.CourseID, turn.Goal, profile, next)
		return err
	})
}
