package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// Teacher routes are read-only: they reuse the learner's own data access and
// never write. Learner routes stay owner-only, so a teacher cannot act for a learner.

type progress struct {
	QuestionsTotal  int `json:"questionsTotal"`
	QuestionsPassed int `json:"questionsPassed"`
	PracticeTotal   int `json:"practiceTotal"`
	PracticePassed  int `json:"practicePassed"`
}

// progress counts embedded questions and practice tasks of the published
// version that have a passed learning attempt.
func (s *server) progress(ctx context.Context, c course) (progress, error) {
	var p progress
	if c.ContentVersionID == nil {
		return p, nil
	}
	v, err := s.Generation.Version(ctx, c.ID, *c.ContentVersionID)
	if err != nil {
		return p, err
	}
	questions, practice := map[string]bool{}, map[string]bool{}
	for _, m := range v.Public.Modules {
		for _, pg := range m.Pages {
			for _, b := range pg.Blocks {
				if b.IsQuestion() {
					questions[b.ID] = true
				}
			}
		}
	}
	for _, t := range v.Public.Practice {
		practice[t.ID] = true
	}
	p.QuestionsTotal, p.PracticeTotal = len(questions), len(practice)
	rows, err := s.DB.Query(ctx, `select distinct item_id from attempts
		where course_id = $1 and content_version_id = $2 and mode = 'learning' and status = 'passed'`, c.ID, *c.ContentVersionID)
	if err != nil {
		return p, err
	}
	passed, err := pgx.CollectRows(rows, pgx.RowTo[string])
	for _, id := range passed {
		if questions[id] {
			p.QuestionsPassed++
		}
		if practice[id] {
			p.PracticePassed++
		}
	}
	return p, err
}

func (s *server) listLearners(w http.ResponseWriter, r *http.Request) {
	type courseSummary struct {
		ID        string    `json:"id"`
		Direction string    `json:"direction"`
		Status    string    `json:"status"`
		Goal      *string   `json:"goal"`
		CreatedAt time.Time `json:"createdAt"`
		Progress  progress  `json:"progress"`
	}
	type learner struct {
		ID        string          `json:"id"`
		Email     string          `json:"email"`
		CreatedAt time.Time       `json:"createdAt"`
		Courses   []courseSummary `json:"courses"`
	}
	rows, err := s.DB.Query(r.Context(), `select id, email, created_at from accounts where not is_teacher order by created_at`)
	if err != nil {
		internalError(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (learner, error) {
		var l learner
		err := row.Scan(&l.ID, &l.Email, &l.CreatedAt)
		l.Courses = []courseSummary{}
		return l, err
	})
	if err != nil {
		internalError(w, r, err)
		return
	}
	byID := map[string]*learner{}
	for i := range list {
		byID[list[i].ID] = &list[i]
	}
	// ponytail: one query per course for progress; aggregate in SQL if the learner list grows large.
	rows, err = s.DB.Query(r.Context(), `select learner_id, `+courseColumns+` from courses order by created_at`)
	if err != nil {
		internalError(w, r, err)
		return
	}
	type owned struct {
		learnerID string
		c         course
	}
	courses, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (owned, error) {
		var o owned
		c := &o.c
		err := row.Scan(&o.learnerID, &c.ID, &c.Direction, &c.Status, &c.Goal, &c.Profile, &c.CreatedAt,
			&c.ConfirmedPlanVersionID, &c.ContentVersionID)
		return o, err
	})
	if err != nil {
		internalError(w, r, err)
		return
	}
	for _, o := range courses {
		l := byID[o.learnerID]
		if l == nil {
			continue // a learner who later became a teacher
		}
		p, err := s.progress(r.Context(), o.c)
		if err != nil {
			internalError(w, r, err)
			return
		}
		l.Courses = append(l.Courses, courseSummary{o.c.ID, o.c.Direction, o.c.Status, o.c.Goal, o.c.CreatedAt, p})
	}
	if list == nil {
		list = []learner{}
	}
	writeJSON(w, http.StatusOK, list)
}

type teacherPlanVersion struct {
	ID          string          `json:"id"`
	Number      int             `json:"number"`
	Plan        json.RawMessage `json:"plan"`
	Explanation string          `json:"explanation"`
	Approved    bool            `json:"approved"`
	Review      json.RawMessage `json:"review"`
	Confirmed   bool            `json:"confirmed"`
	CreatedAt   time.Time       `json:"createdAt"`
}

func (s *server) teacherCourse(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var out struct {
		Learner struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"learner"`
		Course       course               `json:"course"`
		Progress     progress             `json:"progress"`
		Messages     []message            `json:"messages"`
		PlanVersions []teacherPlanVersion `json:"planVersions"`
		Content      any                  `json:"content"`
		Attempts     any                  `json:"attempts"`
		Operations   any                  `json:"operations"`
		Chat         []message            `json:"chat"`
	}
	row := s.DB.QueryRow(ctx, `select a.id, a.email, `+prefixedCourseColumns+` from courses c join accounts a on a.id = c.learner_id
		where c.id::text = $1`, r.PathValue("courseId"))
	c := &out.Course
	err := row.Scan(&out.Learner.ID, &out.Learner.Email, &c.ID, &c.Direction, &c.Status, &c.Goal, &c.Profile, &c.CreatedAt,
		&c.ConfirmedPlanVersionID, &c.ContentVersionID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "Курс не найден")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	c.PracticeAccess.State = "github_not_connected"
	if c.Direction == "pm" {
		c.PracticeAccess.State = "not_required"
	}
	fail := func(err error) bool {
		if err != nil {
			internalError(w, r, err)
		}
		return err != nil
	}
	if c.Operation, err = s.Ops.Current(ctx, c.ID); fail(err) {
		return
	}
	if out.Progress, err = s.progress(ctx, *c); fail(err) {
		return
	}
	if out.Messages, err = s.messages(ctx, c.ID); fail(err) {
		return
	}
	rows, err := s.DB.Query(ctx, `select id, number, plan, explanation, approved, review, coalesce(id = $2, false), created_at
		from plan_versions where course_id = $1 order by number`, c.ID, c.ConfirmedPlanVersionID)
	if fail(err) {
		return
	}
	if out.PlanVersions, err = pgx.CollectRows(rows, pgx.RowToStructByPos[teacherPlanVersion]); fail(err) {
		return
	}
	if out.PlanVersions == nil {
		out.PlanVersions = []teacherPlanVersion{}
	}
	if c.ContentVersionID != nil {
		raw, err := s.Generation.PublicCourse(ctx, c.ID, *c.ContentVersionID)
		if fail(err) {
			return
		}
		out.Content = map[string]any{"versionId": *c.ContentVersionID, "course": json.RawMessage(raw)}
	}
	if out.Attempts, err = s.Learning.List(ctx, c.ID); fail(err) {
		return
	}
	if out.Operations, err = s.Ops.List(ctx, c.ID); fail(err) {
		return
	}
	if out.Chat, err = s.mentorChat(ctx, c.ID); fail(err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// mentorChat is the Learning Mentor conversation of the course.
func (s *server) mentorChat(ctx context.Context, courseID string) ([]message, error) {
	rows, err := s.DB.Query(ctx, `select id, author, text, created_at from chat_messages where course_id = $1 order by created_at, id`, courseID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[message])
	if list == nil {
		list = []message{}
	}
	return list, err
}

const prefixedCourseColumns = `c.id, c.direction, c.status, c.goal, c.profile, c.created_at, c.confirmed_plan_version_id, c.content_version_id`
