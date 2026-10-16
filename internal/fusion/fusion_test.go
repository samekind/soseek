package fusion

import (
	"math"
	"testing"

	"github.com/samekind/soseek/internal/core"
)

func TestCanonicalURLPreservesResourceIdentity(t *testing.T) {
	got := CanonicalURL("HTTPS://Example.COM:443/article?id=1&utm_source=x#part2")
	if got != "https://example.com/article?id=1#part2" {
		t.Fatal(got)
	}
	for _, other := range []string{"http://example.com/article?id=1#part2", "https://example.com/article?id=2#part2", "https://example.com/article/?id=1#part2", "https://example.com/article?id=1#part3"} {
		if CanonicalURL(other) == got {
			t.Fatalf("incorrectly merged %s", other)
		}
	}
}
func TestRRFUsesOneBestRankPerProvider(t *testing.T) {
	input := []core.Source{
		{ID: "S1", URL: "https://a.example/a", Occurrences: []core.Occurrence{{Provider: "exa", Channel: "direct", QueryID: "q1", Rank: 1}}},
		{ID: "S2", URL: "https://b.example/b", Occurrences: []core.Occurrence{{Provider: "exa", Rank: 2}, {Provider: "tavily", Rank: 1}}},
		{URL: "https://a.example/a?utm_source=x", Occurrences: []core.Occurrence{{Provider: "exa", Channel: "proxy", QueryID: "q2", Rank: 3}}},
		{ID: "S3", URL: "https://c.example/c", Occurrences: []core.Occurrence{{Provider: "tavily", Rank: 2}}},
		{ID: "S4", URL: "https://d.example/d", Occurrences: []core.Occurrence{{Provider: "grok"}}},
	}
	out := Merge(input, 0, 0)
	if len(out) != 4 || out[0].ID != "S2" || out[1].ID != "S1" || out[3].ID != "S4" {
		t.Fatalf("bad order: %+v", out)
	}
	if math.Abs(out[1].Score-1.0/61) > 1e-10 {
		t.Fatal("alternative channel/query added a vote", out[1].Score)
	}
	if len(out[1].Occurrences) != 2 {
		t.Fatal("attribution lost")
	}
	if out[3].Score != 0 {
		t.Fatal("invented Grok rank")
	}
}
func TestDomainDiversityUsesPublicSuffixList(t *testing.T) {
	out := Merge([]core.Source{{URL: "https://a.example.co.uk/a"}, {URL: "https://b.example.co.uk/b"}, {URL: "https://c.example.co.uk/c"}, {URL: "https://other.co.uk/d"}}, 0, 2)
	if len(out) != 3 {
		t.Fatalf("expected two from example.co.uk and one other, got %+v", out)
	}
	if Domain("https://x.example.co.uk") != "example.co.uk" {
		t.Fatal("incorrect public suffix handling")
	}
}
