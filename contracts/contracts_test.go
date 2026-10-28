package contracts_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/vladefr97/tutor-app/contracts"
)

func TestOpenAPIIsValid(t *testing.T) {
	raw, err := contracts.FS.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := openapi3.NewLoader().LoadFromData(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAgentResultExamples(t *testing.T) {
	raw, _ := contracts.FS.ReadFile("agent-result.schema.json")
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("agent-result.schema.json", schemaDoc); err != nil {
		t.Fatal(err)
	}
	sch := c.MustCompile("agent-result.schema.json")
	for _, valid := range []bool{true, false} {
		dir := map[bool]string{true: "valid", false: "invalid"}[valid]
		files, _ := filepath.Glob(filepath.Join("examples/agent-result", dir, "*.json"))
		if len(files) == 0 {
			t.Fatalf("no %s examples", dir)
		}
		for _, f := range files {
			t.Run(dir+"/"+filepath.Base(f), func(t *testing.T) {
				doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(mustRead(t, f)))
				if err != nil {
					t.Fatal(err)
				}
				if err := sch.Validate(doc); (err == nil) != valid {
					t.Fatalf("valid=%v, got %v", valid, err)
				}
			})
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
