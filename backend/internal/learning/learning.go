// Package learning records learner attempts on embedded questions and grades
// them: choices against the private answer key, short answers by the Work
// reviewer. The first wrong learning attempt earns the hint, the second the
// explanation.
package learning

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vladefr97/tutor-app/backend/internal/agents"
	"github.com/vladefr97/tutor-app/backend/internal/course"
	"github.com/vladefr97/tutor-app/backend/internal/generation"
)

var (
	ErrInvalid  = errors.New("invalid answer")
	ErrPending  = errors.New("previous answer is still being checked")
	ErrNotFound = errors.New("attempt not found")
	ErrNotReady = errors.New("course is not ready")
)

type Versions interface {
	Version(ctx context.Context, courseID, versionID string) (*generation.Version, error)
}

type Service struct {
	DB            *pgxpool.Pool
	Agent         agents.Agent
	Config        agents.Config
	Versions      Versions
	ReviewTimeout time.Duration
	wg            sync.WaitGroup
}

type Answer struct {
	Options []string `json:"options,omitempty"`
	Text    *string  `json:"text,omitempty"`
}

type Attempt struct {
	ID               string    `json:"id"`
	ItemID           string    `json:"itemId"`
	ContentVersionID string    `json:"contentVersionId"`
	Answer           Answer    `json:"answer"`
	Status           string    `json:"status"`
	Feedback         *string   `json:"feedback"`
	Hint             *string   `json:"hint"`
	Explanation      *string   `json:"explanation"`
	CreatedAt        time.Time `json:"createdAt"`
}

const cols = `id, item_id, content_version_id, answer, status, feedback, hint, explanation, created_at`

func scan(row pgx.Row) (Attempt, error) {
	var a Attempt
	err := row.Scan(&a.ID, &a.ItemID, &a.ContentVersionID, &a.Answer, &a.Status, &a.Feedback, &a.Hint, &a.Explanation, &a.CreatedAt)
	return a, err
}

// question finds an embedded question of the published version.
func question(pub course.Public, id string) (course.Block, bool) {
	for _, m := range pub.Modules {
		for _, p := range m.Pages {
			for _, b := range p.Blocks {
				if b.ID == id && b.IsQuestion() {
					return b, true
				}
			}
		}
	}
	return course.Block{}, false
}

func validAnswer(q course.Block, a Answer) bool {
	if q.Type == "short_answer" {
		return a.Text != nil && strings.TrimSpace(*a.Text) != "" && len([]rune(*a.Text)) <= 4000 && len(a.Options) == 0
	}
	if a.Text != nil || len(a.Options) == 0 || (q.Type == "single_choice" && len(a.Options) != 1) {
		return false
	}
	seen := map[string]bool{}
	for _, o := range a.Options {
		if seen[o] || !slices.ContainsFunc(q.Options, func(opt course.Option) bool { return opt.ID == o }) {
			return false
		}
		seen[o] = true
	}
	return true
}

// Submit stores an attempt for the course's current version and grades it.
func (s *Service) Submit(ctx context.Context, courseID, itemID string, ans Answer) (Attempt, error) {
	var versionID *string
	var profile json.RawMessage
	if err := s.DB.QueryRow(ctx, `select content_version_id, coalesce(profile, '{}') from courses where id = $1`, courseID).Scan(&versionID, &profile); err != nil {
		return Attempt{}, err
	}
	if versionID == nil {
		return Attempt{}, ErrNotReady
	}
	v, err := s.Versions.Version(ctx, courseID, *versionID)
	if err != nil {
		return Attempt{}, err
	}
	q, ok := question(v.Public, itemID)
	if !ok || !validAnswer(q, ans) {
		return Attempt{}, ErrInvalid
	}
	raw, _ := json.Marshal(ans)
	at, err := scan(s.DB.QueryRow(ctx, `insert into attempts (course_id, content_version_id, item_id, item_type, answer)
		values ($1, $2, $3, $4, $5) returning `+cols, courseID, *versionID, itemID, q.Type, raw))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Attempt{}, ErrPending
	}
	if err != nil {
		return Attempt{}, err
	}
	key := v.Private.Questions[itemID]
	if q.Type != "short_answer" {
		correct := slices.Clone(key.Correct)
		got := slices.Clone(ans.Options)
		slices.Sort(correct)
		slices.Sort(got)
		return s.grade(ctx, at.ID, slices.Equal(correct, got), nil, key)
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.reviewShortAnswer(at.ID, courseID, v.Public.Direction, q, key, *ans.Text, profile)
	}()
	return at, nil
}

// grade writes the result to a pending attempt only, adding the hint after the
// first failed learning attempt and the explanation after the second.
func (s *Service) grade(ctx context.Context, attemptID string, passed bool, feedback *string, key course.QuestionKey) (Attempt, error) {
	var at Attempt
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var courseID, itemID, versionID string
		if err := tx.QueryRow(ctx, `select course_id, item_id, content_version_id from attempts where id = $1 and status = 'pending' for update`,
			attemptID).Scan(&courseID, &itemID, &versionID); err != nil {
			return err
		}
		status := "passed"
		var hint, explanation *string
		if !passed {
			status = "failed"
			var failed int
			if err := tx.QueryRow(ctx, `select count(*) from attempts where course_id = $1 and item_id = $2 and content_version_id = $3
				and mode = 'learning' and status = 'failed'`, courseID, itemID, versionID).Scan(&failed); err != nil {
				return err
			}
			if failed == 0 {
				hint = &key.Hint
			} else {
				explanation = &key.Explanation
			}
		}
		var err error
		at, err = scan(tx.QueryRow(ctx, `update attempts set status = $2, feedback = $3, hint = $4, explanation = $5, graded_at = now()
			where id = $1 returning `+cols, attemptID, status, feedback, hint, explanation))
		return err
	})
	return at, err
}

type workReview struct {
	Verdict  string `json:"verdict"`
	Feedback string `json:"feedback"`
}

// reviewShortAnswer asks the Work reviewer in a fresh session with only this
// question, its criteria and the answer.
func (s *Service) reviewShortAnswer(attemptID, courseID, direction string, q course.Block, key course.QuestionKey, text string, profile json.RawMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), s.ReviewTimeout)
	defer cancel()
	res, err := s.review(ctx, courseID, direction, map[string]any{"mode": "short_answer", "exam": false,
		"question": q.Prompt, "criteria": key.Criteria, "answer": text, "profile": profile})
	if err != nil {
		slog.Error("short answer review", "attempt", attemptID, "err", err)
		msg := "Не удалось проверить ответ. Отправьте его ещё раз."
		s.DB.Exec(context.Background(), `update attempts set status = 'error', feedback = $2, graded_at = now()
			where id = $1 and status = 'pending'`, attemptID, msg)
		return
	}
	raw, _ := json.Marshal(res)
	s.DB.Exec(context.Background(), `update attempts set review = $2 where id = $1 and status = 'pending'`, attemptID, raw)
	if _, err := s.grade(context.Background(), attemptID, res.Verdict == "passed", &res.Feedback, key); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Error("short answer grade", "attempt", attemptID, "err", err)
	}
}

func (s *Service) review(ctx context.Context, courseID, direction string, input map[string]any) (workReview, error) {
	var rv workReview
	var n int
	if err := s.DB.QueryRow(ctx, `select count(*) + 1 from agent_sessions where course_id = $1 and role = $2`, courseID, agents.WorkReviewer).Scan(&n); err != nil {
		return rv, err
	}
	dir := s.Config.SessionDir(courseID, agents.WorkReviewer, n)
	model := s.Config.Models[agents.WorkReviewer]
	var sessionID string
	if err := s.DB.QueryRow(ctx, `insert into agent_sessions (course_id, role, dir, model) values ($1, $2, $3, $4) returning id`,
		courseID, agents.WorkReviewer, dir, model).Scan(&sessionID); err != nil {
		return rv, err
	}
	instructions, err := s.Config.Instructions(agents.WorkReviewer, direction)
	if err != nil {
		return rv, err
	}
	in, _ := json.Marshal(input)
	type outcome struct {
		res agents.TurnResult
		err error
	}
	// The deadline holds even if the provider ignores cancellation; a late
	// result is discarded.
	done := make(chan outcome, 1)
	go func() {
		res, err := s.Agent.Turn(ctx, agents.TurnRequest{Role: agents.WorkReviewer, SessionDir: dir, Model: model,
			Instructions: instructions, Input: string(in), OutputSchema: agents.OutputSchema(agents.KindWorkReview)})
		done <- outcome{res, err}
	}()
	var res agents.TurnResult
	select {
	case <-ctx.Done():
		return rv, agents.ErrTimeout
	case o := <-done:
		res, err = o.res, o.err
	}
	if res.ThreadID != "" {
		s.DB.Exec(context.Background(), `update agent_sessions set thread_id = $2 where id = $1`, sessionID, res.ThreadID)
	}
	if err != nil {
		return rv, err
	}
	err = agents.ParseResult(agents.KindWorkReview, res.Text, &rv)
	return rv, err
}

func (s *Service) List(ctx context.Context, courseID string) ([]Attempt, error) {
	rows, err := s.DB.Query(ctx, `select `+cols+` from attempts where course_id = $1 order by created_at, id`, courseID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Attempt, error) { return scan(r) })
	if list == nil {
		list = []Attempt{}
	}
	return list, err
}

// Get returns an attempt of a course owned by learnerID.
func (s *Service) Get(ctx context.Context, attemptID, learnerID string) (Attempt, error) {
	at, err := scan(s.DB.QueryRow(ctx, `select a.id, a.item_id, a.content_version_id, a.answer, a.status, a.feedback, a.hint,
		a.explanation, a.created_at from attempts a join courses c on c.id = a.course_id
		where a.id::text = $1 and c.learner_id = $2`, attemptID, learnerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return at, ErrNotFound
	}
	return at, err
}

// FailInterrupted marks attempts left pending by a previous process as errors.
func (s *Service) FailInterrupted(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, `update attempts set status = 'error', feedback = 'Проверка прервалась. Отправьте ответ ещё раз.',
		graded_at = now() where status = 'pending'`)
	return err
}

func (s *Service) Wait() { s.wg.Wait() }
