package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vladefr97/tutor-app/backend/internal/ops"
	"github.com/vladefr97/tutor-app/backend/internal/planning"
)

type direction struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

var directions = []direction{
	{"python", "Python Developer", "Python с нуля до уверенных функций, тестов и небольших программ"},
	{"git", "Наставник по Git", "Коммиты, ветки, pull request и работа в команде"},
	{"pm", "Project Manager", "Цели, задачи, сроки и риски небольшого проекта"},
}

type practiceAccess struct {
	State         string  `json:"state"`
	RepoURL       *string `json:"repoUrl"`
	InvitationURL *string `json:"invitationUrl"`
}

type course struct {
	ID                     string            `json:"id"`
	Direction              string            `json:"direction"`
	Status                 string            `json:"status"`
	Goal                   *string           `json:"goal"`
	Profile                *planning.Profile `json:"profile"`
	CreatedAt              time.Time         `json:"createdAt"`
	ConfirmedPlanVersionID *string           `json:"confirmedPlanVersionId"`
	ContentVersionID       *string           `json:"contentVersionId"`
	PracticeAccess         practiceAccess    `json:"practiceAccess"`
	Operation              *ops.Op           `json:"operation"`
}

func (s *server) directions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, directions)
}

const courseColumns = `id, direction, status, goal, profile, created_at, confirmed_plan_version_id`

func scanCourse(row pgx.Row) (course, error) {
	var c course
	err := row.Scan(&c.ID, &c.Direction, &c.Status, &c.Goal, &c.Profile, &c.CreatedAt, &c.ConfirmedPlanVersionID)
	c.PracticeAccess.State = "github_not_connected"
	if c.Direction == "pm" {
		c.PracticeAccess.State = "not_required"
	}
	return c, err
}

func (s *server) createCourse(w http.ResponseWriter, r *http.Request) {
	var in struct{ Direction string }
	if !readJSON(w, r, &in) {
		return
	}
	if in.Direction != "python" && in.Direction != "git" && in.Direction != "pm" {
		writeError(w, http.StatusBadRequest, "invalid_direction", "Неизвестное направление")
		return
	}
	c, err := scanCourse(s.DB.QueryRow(r.Context(), `insert into courses (learner_id, direction) values ($1, $2)
		returning `+courseColumns, accountFrom(r.Context()).ID, in.Direction))
	if err != nil {
		internalError(w, r, err)
		return
	}
	op, err := s.Planning.StartInterview(r.Context(), c.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	c.Operation = &op
	writeJSON(w, http.StatusCreated, c)
}

func (s *server) listCourses(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.Query(r.Context(), `select `+courseColumns+` from courses where learner_id = $1 order by created_at`,
		accountFrom(r.Context()).ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	defer rows.Close()
	list := []course{}
	for rows.Next() {
		c, err := scanCourse(rows)
		if err != nil {
			internalError(w, r, err)
			return
		}
		if c.Operation, err = s.Ops.Current(r.Context(), c.ID); err != nil {
			internalError(w, r, err)
			return
		}
		list = append(list, c)
	}
	writeJSON(w, http.StatusOK, list)
}

// ownCourse loads a course of the current learner; others' courses look absent.
func (s *server) ownCourse(w http.ResponseWriter, r *http.Request) (course, bool) {
	c, err := scanCourse(s.DB.QueryRow(r.Context(), `select `+courseColumns+` from courses
		where id::text = $1 and learner_id = $2`, r.PathValue("courseId"), accountFrom(r.Context()).ID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "Курс не найден")
		return c, false
	}
	if err == nil {
		c.Operation, err = s.Ops.Current(r.Context(), c.ID)
	}
	if err != nil {
		internalError(w, r, err)
		return c, false
	}
	return c, true
}

func (s *server) getCourse(w http.ResponseWriter, r *http.Request) {
	if c, ok := s.ownCourse(w, r); ok {
		writeJSON(w, http.StatusOK, c)
	}
}

func (s *server) listLearners(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.Query(r.Context(), `select id, email, created_at from accounts where not is_teacher order by created_at`)
	if err != nil {
		internalError(w, r, err)
		return
	}
	type learner struct {
		ID        string    `json:"id"`
		Email     string    `json:"email"`
		CreatedAt time.Time `json:"createdAt"`
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[learner])
	if err != nil {
		internalError(w, r, err)
		return
	}
	if list == nil {
		list = []learner{}
	}
	writeJSON(w, http.StatusOK, list)
}
