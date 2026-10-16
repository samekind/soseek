package engine

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/samekind/soseek/internal/core"
)

// Optional, offline reproduction against an operator-supplied saved result.
// It never contacts an upstream or embeds a private result in the repository.
func TestInspectSavedEvidence(t *testing.T) {
	path, query := os.Getenv("SOSEEK_INSPECT_FIXTURE"), os.Getenv("SOSEEK_INSPECT_QUERY")
	if path == "" || query == "" {
		t.Skip("provide a saved result and inspect query to replay locally")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Result core.Result `json:"result"`
	}
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Result.Results) == 0 {
		t.Fatal("saved result contains no evidence")
	}
	e, _, _ := fixture()
	s := &execution{e: e, sources: saved.Result.Results}
	value, err := s.inspect(inspectArgs{SourceID: saved.Result.Results[0].ID, Query: query})
	if err != nil {
		t.Fatal(err)
	}
	view := value.(map[string]any)
	if view["query_matched"] != true {
		t.Fatal("query did not match saved evidence")
	}
	passages := view["passages"].([]excerpt)
	expected := os.Getenv("SOSEEK_INSPECT_EXPECT")
	if expected != "" {
		found := false
		for _, p := range passages {
			if len(matchOffsets([]rune(p.Text), expected, 0)) > 0 {
				found = true
			}
		}
		if !found {
			t.Fatal("expected normalized evidence is absent from returned passages")
		}
	}
	t.Logf("matched=%v offsets=%v passages=%d; all returned passages retain original offsets", view["match_count"], view["matched_offsets"], len(passages))
}
