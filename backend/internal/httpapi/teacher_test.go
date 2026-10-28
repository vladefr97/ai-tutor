package httpapi_test

import (
	"encoding/json"
	"strings"
	"testing"
)

type teacherCourse struct {
	Learner  struct{ ID, Email string } `json:"learner"`
	Course   courseFull                 `json:"course"`
	Messages []chatMessage              `json:"messages"`
	Attempts json.RawMessage            `json:"attempts"`
	Plans    []struct {
		ID       string          `json:"id"`
		Approved bool            `json:"approved"`
		Review   json.RawMessage `json:"review"`
	} `json:"planVersions"`
	Content *contentResp  `json:"content"`
	Chat    []chatMessage `json:"chat"`
}

type learnerSummary struct {
	Email   string `json:"email"`
	Courses []struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Progress struct {
			QuestionsTotal  int `json:"questionsTotal"`
			QuestionsPassed int `json:"questionsPassed"`
		} `json:"progress"`
	} `json:"courses"`
}

func TestLearnerIsRefusedOnEveryTeacherRoute(t *testing.T) {
	h := newHarness(t)
	a := h.login("a@example.com")
	n := 0
	for path, item := range spec.Paths.Map() {
		if !strings.HasPrefix(path, "/teacher/") {
			continue
		}
		url := "/api" + strings.ReplaceAll(path, "{courseId}", "00000000-0000-0000-0000-000000000001")
		for method := range item.Operations() {
			n++
			if st, _ := a.do(method, url, nil); st != 403 {
				t.Errorf("%s %s: learner got %d, want 403", method, url, st)
			}
		}
	}
	if n < 3 {
		t.Fatalf("only %d teacher routes found in openapi.yaml", n)
	}
}

func TestTeacherSeesExactlyWhatTheLearnerSent(t *testing.T) {
	h, _, a, courseID := readyPM(t)
	a.answer(courseID, "q-goal-single", opts("a"))
	a.answer(courseID, "q-risks-multi", opts("a", "b"))
	_, learnerAttempts := a.do("GET", "/api/courses/"+courseID+"/attempts", nil)
	learnerMessages := a.messages(courseID)

	teacher := h.login(adminEmail)
	_, b := teacher.do("GET", "/api/teacher/learners", nil)
	var found bool
	for _, l := range decode[[]learnerSummary](t, b) {
		if l.Email != "a@example.com" {
			continue
		}
		found = true
		if len(l.Courses) != 1 || l.Courses[0].ID != courseID || l.Courses[0].Status != "ready" ||
			l.Courses[0].Progress.QuestionsTotal != 3 || l.Courses[0].Progress.QuestionsPassed != 1 {
			t.Fatalf("learner summary: %s", b)
		}
	}
	if !found {
		t.Fatalf("learner missing: %s", b)
	}

	st, b := teacher.do("GET", "/api/teacher/courses/"+courseID, nil)
	if st != 200 {
		t.Fatalf("teacher course: %d %s", st, b)
	}
	tc := decode[teacherCourse](t, b)
	if tc.Learner.Email != "a@example.com" || tc.Course.ID != courseID {
		t.Fatalf("course header: %+v", tc)
	}
	if len(tc.Messages) != len(learnerMessages) {
		t.Fatalf("interview: %+v vs %+v", tc.Messages, learnerMessages)
	}
	for i := range learnerMessages {
		if tc.Messages[i] != learnerMessages[i] {
			t.Fatalf("message %d: %+v vs %+v", i, tc.Messages[i], learnerMessages[i])
		}
	}
	var want, got any
	json.Unmarshal(learnerAttempts, &want)
	json.Unmarshal(tc.Attempts, &got)
	wb, _ := json.Marshal(want)
	gb, _ := json.Marshal(got)
	if string(wb) != string(gb) {
		t.Fatalf("attempts differ:\nlearner %s\nteacher %s", wb, gb)
	}
	if len(tc.Plans) == 0 || !tc.Plans[len(tc.Plans)-1].Approved || len(tc.Plans[len(tc.Plans)-1].Review) < 5 {
		t.Fatalf("plan versions with review: %s", b)
	}
	if tc.Content == nil || !strings.Contains(string(tc.Content.Course), "Основы управления проектом") || tc.Chat == nil {
		t.Fatalf("content/chat: %s", b)
	}
}

func TestTeacherCannotActForTheLearner(t *testing.T) {
	h, _, _, courseID := readyPM(t)
	teacher := h.login(adminEmail)
	if st, _ := teacher.answer(courseID, "q-goal-single", opts("b")); st != 404 {
		t.Fatalf("teacher submitted an answer: %d", st)
	}
	if st, _ := teacher.do("POST", "/api/courses/"+courseID+"/messages", map[string]string{"text": "x"}); st != 404 {
		t.Fatalf("teacher wrote to the mentor: %d", st)
	}
	if st, _ := teacher.do("GET", "/api/teacher/courses/00000000-0000-0000-0000-000000000009", nil); st != 404 {
		t.Fatalf("unknown course: %d", st)
	}
}
