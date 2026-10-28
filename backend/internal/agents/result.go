package agents

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/vladefr97/tutor-app/contracts"
)

// Result kinds from contracts/agent-result.schema.json.
const (
	KindInterviewTurn = "interview_turn"
	KindPlanProposal  = "plan_proposal"
	KindPlanReview    = "plan_review"
	KindContentReview = "content_review"
	KindWorkReview    = "work_review"
)

var (
	resultOnce   sync.Once
	resultDoc    map[string]any
	resultSchema *jsonschema.Schema
)

func loadResultSchema() {
	resultOnce.Do(func() {
		raw, err := contracts.FS.ReadFile("agent-result.schema.json")
		if err != nil {
			panic(err)
		}
		if err := json.Unmarshal(raw, &resultDoc); err != nil {
			panic(err)
		}
		doc, _ := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		c := jsonschema.NewCompiler()
		if err := c.AddResource("agent-result.schema.json", doc); err != nil {
			panic(err)
		}
		resultSchema = c.MustCompile("agent-result.schema.json")
	})
}

func defName(kind string) string {
	parts := strings.Split(kind, "_")
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

// OutputSchema is the schema for one result kind in the subset accepted by
// strict structured outputs: refs inlined, conditional and length keywords
// dropped. The full schema is still enforced by ParseResult.
func OutputSchema(kind string) json.RawMessage {
	loadResultSchema()
	defs := resultDoc["$defs"].(map[string]any)
	def, ok := defs[defName(kind)]
	if !ok {
		panic("unknown result kind " + kind)
	}
	b, _ := json.Marshal(strict(def, defs))
	return b
}

var dropped = map[string]bool{"if": true, "then": true, "else": true, "allOf": true, "minLength": true,
	"minItems": true, "maxItems": true, "uniqueItems": true, "pattern": true, "$schema": true, "$id": true}

func strict(v any, defs map[string]any) any {
	switch t := v.(type) {
	case map[string]any:
		if ref, ok := t["$ref"].(string); ok {
			return strict(defs[strings.TrimPrefix(ref, "#/$defs/")], defs)
		}
		out := map[string]any{}
		for k, val := range t {
			switch {
			case dropped[k]:
			case k == "const":
				out["enum"] = []any{val}
			case k == "properties":
				props := map[string]any{}
				for name, p := range val.(map[string]any) {
					props[name] = strict(p, defs)
				}
				out[k] = props
			default:
				out[k] = strict(val, defs)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = strict(x, defs)
		}
		return out
	}
	return v
}

// ParseResult validates the agent's final message against the full schema and
// decodes it. A message that does not validate is an error, never a pass.
func ParseResult(kind, text string, into any) error {
	loadResultSchema()
	text = strings.TrimSpace(text)
	text = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(text, "```json"), "```"), "```")
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
	if err != nil {
		return fmt.Errorf("agent result is not JSON: %w", err)
	}
	if m, ok := doc.(map[string]any); !ok || m["kind"] != kind {
		return fmt.Errorf("agent result: want kind %s", kind)
	}
	if err := resultSchema.Validate(doc); err != nil {
		return fmt.Errorf("agent result violates schema: %w", err)
	}
	return json.Unmarshal([]byte(text), into)
}
