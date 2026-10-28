package learning

import (
	"encoding/json"
	"testing"

	"github.com/vladefr97/tutor-app/backend/internal/course"
	"github.com/vladefr97/tutor-app/contracts"
)

// Code tasks are submitted through pull requests, never as site attempts.
func TestOnlyCaseTasksAreSubmittedOnTheSite(t *testing.T) {
	for _, tc := range []struct {
		example, id string
		want        bool
	}{{"pm-basics", "t-plan-case", true}, {"python-functions", "t-sum-list", false}} {
		raw, err := contracts.FS.ReadFile("examples/course/valid/" + tc.example + "/course.json")
		if err != nil {
			t.Fatal(err)
		}
		var pub course.Public
		json.Unmarshal(raw, &pub)
		if _, ok := practiceCase(pub, tc.id); ok != tc.want {
			t.Fatalf("%s/%s: practiceCase = %v", tc.example, tc.id, ok)
		}
	}
}
