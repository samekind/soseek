package core

import (
	"strings"
	"testing"
)

func TestSourceWindowsPreserveUnicodeAndTerminate(t *testing.T) {
	source := Source{ID: "s1", URL: "https://example.org", Content: "一🙂e\u0301二", Snippet: "not the content", Truncated: true}
	var joined strings.Builder
	offset := 0
	for n := 0; ; n++ {
		if n > 3 {
			t.Fatal("paging did not terminate")
		}
		w, err := WindowSource("run_1", source, offset, 2)
		if err != nil {
			t.Fatal(err)
		}
		if w.Field != "content" || w.TotalChars != 5 || !w.UpstreamTruncated || w.Offset != offset || w.ReturnedChars != len([]rune(w.Text)) {
			t.Fatalf("bad window: %+v", w)
		}
		joined.WriteString(w.Text)
		if w.NextOffset == nil {
			break
		}
		offset = *w.NextOffset
	}
	if joined.String() != source.Content {
		t.Fatal("paging changed evidence")
	}
	w, err := WindowSource("run_1", source, 5, 2)
	if err != nil || w.Text != "" || w.NextOffset != nil {
		t.Fatalf("end: %+v %v", w, err)
	}
	for _, pair := range [][2]int{{-1, 2}, {6, 2}, {0, -1}, {0, MaxSourceChars + 1}} {
		if _, err := WindowSource("run_1", source, pair[0], pair[1]); err == nil {
			t.Fatalf("accepted %v", pair)
		}
	}
}

func TestSourceSnippetAndDefaultWindow(t *testing.T) {
	w, err := WindowSource("run_1", Source{ID: "s1", Snippet: strings.Repeat("字", 4001)}, 0, 0)
	if err != nil || w.Field != "snippet" || w.ReturnedChars != 4000 || w.TotalChars != 4001 || w.NextOffset == nil || *w.NextOffset != 4000 || !w.Truncated || w.UpstreamTruncated {
		t.Fatalf("%+v %v", w, err)
	}
	w, err = WindowSource("run_1", Source{ID: "s1"}, 0, 0)
	if err != nil || w.Text != "" || w.NextOffset != nil || w.Truncated {
		t.Fatalf("empty: %+v %v", w, err)
	}
}
