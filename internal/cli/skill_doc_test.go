package cli

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/priyanshu-shubham/jenkinsctl/internal/jenkins"
)

// TestSkillDocumentsResponseShapes keeps the shapes in SKILL.md in sync with
// what the CLI actually emits: every field must be documented, and nothing
// documented may be missing from the output.
func TestSkillDocumentsResponseShapes(t *testing.T) {
	doc, err := os.ReadFile("../../skills/jenkinsctl/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	blocks := map[string]string{}
	for _, m := range regexp.MustCompile("(?s)```json (\\w+)\n(.*?)```").FindAllStringSubmatch(string(doc), -1) {
		blocks[m[1]] = m[2]
	}

	// Every field populated so omitempty fields show up.
	actual := map[string]any{
		"run": &jenkins.Build{
			Job: "j", Number: 1, URL: "u", Status: "FAILURE", Result: "FAILURE", StartedAt: time.Now(),
			DurationMs: 1, EstimatedDurationMs: 1, Description: "d", QueueID: 1,
			Parameters: map[string]any{"BRANCH": "main"},
			Causes:     []jenkins.Cause{{Description: "c", UserID: "u"}},
			LogTail:    []string{"l"},
		},
		"queued": queued{Job: "j", Status: "QUEUED", QueueID: 1, QueueURL: "u", Why: "w"},
		"job": &jenkins.Job{FullName: "j", URL: "u", Buildable: true, LastBuild: 1, Params: []jenkins.ParamDef{
			{Name: "n", Type: "t", Description: "d", Default: "x", Choices: []string{"x"}},
		}},
		"servers": []serverEntry{{Name: "n", URL: "u", User: "u", Insecure: true}},
	}

	for name, v := range actual {
		block, ok := blocks[name]
		if !ok {
			t.Errorf("SKILL.md has no ```json %s block", name)
			continue
		}
		var documented any
		if err := json.Unmarshal([]byte(block), &documented); err != nil {
			t.Errorf("%s example is not valid JSON: %v", name, err)
			continue
		}
		b, _ := json.Marshal(v)
		var emitted any
		_ = json.Unmarshal(b, &emitted)
		if got, want := keyPaths(documented), keyPaths(emitted); got != want {
			t.Errorf("%s shape drifted from the code\n documented: %s\n emitted:    %s", name, got, want)
		}
		delete(blocks, name)
	}
	for name := range blocks {
		t.Errorf("SKILL.md documents %q but no test checks it", name)
	}
}

func keyPaths(v any) string {
	set := map[string]bool{}
	var walk func(v any, prefix string)
	walk = func(v any, prefix string) {
		switch t := v.(type) {
		case map[string]any:
			for k, c := range t {
				set[prefix+k] = true
				walk(c, prefix+k+".")
			}
		case []any:
			for _, c := range t {
				walk(c, strings.TrimSuffix(prefix, ".")+"[].")
			}
		}
	}
	walk(v, "")
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, " ")
}
