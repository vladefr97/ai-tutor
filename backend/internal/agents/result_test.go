package agents_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vladefr97/tutor-app/backend/internal/agents"
)

func TestOutputSchemaIsSelfContainedAndStrict(t *testing.T) {
	for _, kind := range []string{agents.KindInterviewTurn, agents.KindPlanProposal, agents.KindPlanReview, agents.KindContentReview, agents.KindWorkReview} {
		s := string(agents.OutputSchema(kind))
		for _, banned := range []string{`"$ref"`, `"if"`, `"const"`, `"minLength"`} {
			if strings.Contains(s, banned) {
				t.Errorf("%s: output schema contains %s", kind, banned)
			}
		}
		if !strings.Contains(s, `"enum":["`+kind+`"]`) {
			t.Errorf("%s: kind is not pinned: %s", kind, s)
		}
	}
}

func TestParseResultRejectsWhatTheFullSchemaRejects(t *testing.T) {
	var v map[string]any
	ok := `{"kind":"interview_turn","message":"Привет","goal":null,"profile":{"preparation":null,"context":null,"preferences":null},"readyForPlan":false}`
	if err := agents.ParseResult(agents.KindInterviewTurn, ok, &v); err != nil {
		t.Fatal(err)
	}
	readyWithoutGoal := strings.Replace(ok, `"readyForPlan":false`, `"readyForPlan":true`, 1)
	if err := agents.ParseResult(agents.KindInterviewTurn, readyWithoutGoal, &v); err == nil {
		t.Fatal("readyForPlan without goal accepted")
	}
	if err := agents.ParseResult(agents.KindPlanReview, ok, &v); err == nil {
		t.Fatal("wrong kind accepted")
	}
	if err := agents.ParseResult(agents.KindInterviewTurn, "не JSON", &v); err == nil {
		t.Fatal("free text accepted")
	}
	var anyJSON any
	if json.Unmarshal(agents.OutputSchema(agents.KindWorkReview), &anyJSON) != nil {
		t.Fatal("output schema is not JSON")
	}
}
