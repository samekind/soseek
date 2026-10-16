package engine

import (
	"strings"
	"unicode"

	"github.com/samekind/soseek/internal/core"
	"github.com/samekind/soseek/internal/fusion"
)

type inspectArgs struct {
	SourceID string `json:"source_id"`
	Query    string `json:"query,omitempty"`
	Offset   int    `json:"offset,omitempty"`
	MaxChars int    `json:"max_chars,omitempty"`
}
type excerpt struct {
	SourceID string `json:"source_id"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Text     string `json:"text"`
}

// inspect reads only already fetched evidence in this execution. It neither
// fetches URLs nor queries a store where another principal's content can exist.
func (s *execution) inspect(a inspectArgs) (any, error) {
	if a.SourceID == "" || a.Offset < 0 || a.MaxChars < 0 || a.MaxChars > 6000 || len(a.Query) > 1000 {
		return nil, core.E("invalid_tool_call", "inspect requires source_id, a nonnegative character offset, and max_chars at most 6000")
	}
	if a.MaxChars == 0 {
		a.MaxChars = 4000
	}
	var source core.Source
	for _, candidate := range fusion.Merge(s.sources, 0, 0) {
		if candidate.ID == a.SourceID {
			source = candidate
			break
		}
	}
	if source.ID == "" || source.Content == "" {
		return nil, core.E("evidence_not_read", "source is not an already fetched full-text source in this run; use read first")
	}
	runes := []rune(source.Content)
	total := len(runes)
	if a.Offset > total {
		return nil, core.E("invalid_tool_call", "inspect offset is beyond the available document")
	}
	if a.Offset == total {
		return map[string]any{"source_id": source.ID, "content_total_chars": total, "start": total, "end": total, "next_offset": total, "has_more": false, "untrusted_evidence": ""}, nil
	}
	remainingBudget := 24000 - s.excerptBytes
	if remainingBudget < 4 {
		return nil, core.E("inspection_limit", "selected evidence has reached its bounded context budget; finish with the passages already selected")
	}
	// A UTF-8 rune uses at most four bytes. Reserve enough context for the
	// complete returned passage, including a matched term near its middle.
	a.MaxChars = min(a.MaxChars, max(1, remainingBudget/4))
	matches := []int{}
	if a.Query != "" {
		matches = matchOffsets(runes, a.Query, a.Offset)
		if len(matches) == 0 {
			return map[string]any{"source_id": source.ID, "query": a.Query, "query_matched": false, "match_count": 0, "content_total_chars": total, "available_text_truncated": source.Truncated, "suggested_queries": shorterQueries(a.Query), "instructions": "No match after case and whitespace normalization. Shorten the query to one distinctive word or inspect by character offset; do not repeat the identical phrase or refetch this already saved document."}, nil
		}
	}
	// Return several non-overlapping hits so a table-of-contents occurrence
	// cannot hide the later section. The total passage length stays bounded.
	window := a.MaxChars
	if len(matches) > 1 {
		window = max(1, a.MaxChars/min(3, len(matches)))
	}
	starts := []int{a.Offset}
	usedMatches := []int{}
	headingMatches := []bool{}
	if len(matches) > 0 {
		starts = nil
		lastEnd := -1
		for _, position := range matches {
			start := max(a.Offset, position-window/3)
			// A heading match should expose the section body, not spend most
			// of its window on the preceding section or table of contents.
			lineStart := position
			for lineStart > a.Offset && runes[lineStart-1] != '\n' {
				lineStart--
			}
			prefix := strings.TrimSpace(string(runes[lineStart:position]))
			heading := strings.HasPrefix(prefix, "#")
			if heading {
				start = lineStart
			}
			if start < lastEnd {
				continue
			}
			starts = append(starts, start)
			usedMatches = append(usedMatches, position)
			headingMatches = append(headingMatches, heading)
			lastEnd = min(total, start+window)
			if len(starts) == min(3, a.MaxChars) {
				break
			}
		}
	}
	weightTotal := len(starts)
	for _, heading := range headingMatches {
		if heading {
			weightTotal++
		}
	}
	passages := []excerpt{}
	continuations := []map[string]any{}
	for i, start := range starts {
		passageWindow := window
		heading := i < len(headingMatches) && headingMatches[i]
		if len(matches) > 0 {
			weight := 1
			if heading {
				weight = 2
			}
			passageWindow = max(1, a.MaxChars*weight/max(1, weightTotal))
			if !heading {
				start = max(a.Offset, usedMatches[i]-passageWindow/3)
			}
		}
		end := min(total, start+passageWindow)
		text := string(runes[start:end])
		if text == "" {
			continue
		}
		selected := excerpt{SourceID: source.ID, Start: start, End: end, Text: text}
		savedAlready := false
		for _, saved := range s.excerpts {
			if saved.SourceID == selected.SourceID && saved.Start == selected.Start && saved.End == selected.End {
				savedAlready = true
				break
			}
		}
		if !savedAlready {
			s.excerpts = append(s.excerpts, selected)
			s.excerptBytes += len(text)
		}
		passages = append(passages, selected)
		continuations = append(continuations, map[string]any{"source_id": source.ID, "start": start, "end": end, "next_offset": end, "has_more": end < total, "heading_match": heading})
	}
	if len(passages) == 0 {
		return nil, core.E("inspection_limit", "no selected-evidence context remains")
	}
	var nextMatch *int
	if len(usedMatches) > 0 {
		last := usedMatches[len(usedMatches)-1]
		for _, position := range matches {
			if position > last && position >= passages[len(passages)-1].End {
				p := position
				nextMatch = &p
				break
			}
		}
	}
	view := inspectionResult(source, passages[0], total, true).(map[string]any)
	view["passages"] = passages
	view["passage_continuations"] = continuations
	view["match_count"] = len(matches)
	view["matched_offsets"] = usedMatches
	view["next_match_offset"] = nextMatch
	view["more_matches"] = nextMatch != nil
	view["matching"] = "case_insensitive_whitespace_normalized"
	view["instructions"] = "Review all returned passages; the first hit may be a table of contents. Body headings receive more context but each passage remains an excerpt. Passages use original document character offsets and are saved for final synthesis. Use next_match_offset to find later matches, or the corresponding passage_continuations.next_offset to read further into that section."
	return view, nil
}

// The index map preserves original character offsets while collapsing spaces,
// tabs and line breaks for matching. Returned evidence remains untouched.
func normalizedRunes(input []rune) ([]rune, []int) {
	out := []rune{}
	positions := []int{}
	space := false
	for i, r := range input {
		if unicode.IsSpace(r) {
			if space {
				continue
			}
			r = ' '
			space = true
		} else {
			space = false
			r = unicode.ToLower(r)
		}
		out = append(out, r)
		positions = append(positions, i)
	}
	return out, positions
}
func matchOffsets(input []rune, query string, offset int) []int {
	hay, positions := normalizedRunes(input[offset:])
	needle, _ := normalizedRunes([]rune(strings.TrimSpace(query)))
	if len(needle) == 0 {
		return nil
	}
	matches := []int{}
	for i := 0; i+len(needle) <= len(hay); i++ {
		same := true
		for j, r := range needle {
			if hay[i+j] != r {
				same = false
				break
			}
		}
		if same {
			matches = append(matches, offset+positions[i])
			i += len(needle) - 1
		}
	}
	return matches
}
func shorterQueries(query string) []string {
	out := []string{}
	for _, word := range strings.FieldsFunc(query, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) }) {
		if len([]rune(word)) < 4 {
			continue
		}
		if !has(out, word) {
			out = append(out, word)
		}
		if len(out) == 4 {
			break
		}
	}
	return out
}
func inspectionResult(source core.Source, selected excerpt, total int, matched bool) any {
	return map[string]any{"source_id": source.ID, "untrusted_evidence": selected.Text, "start": selected.Start, "end": selected.End, "next_offset": selected.End, "has_more": selected.End < total, "query_matched": matched, "content_total_chars": total, "available_text_truncated": source.Truncated, "saved_for_final_answer": true}
}

func (s *execution) selectedSources() []core.Source {
	selected := fusion.Merge(s.sources, s.run.Request.Limit, domainLimit(s.run.Request))
	ids := map[string]bool{}
	for _, source := range selected {
		ids[source.ID] = true
	}
	wanted := map[string]bool{}
	for _, x := range s.excerpts {
		wanted[x.SourceID] = true
	}
	for _, citation := range s.citations {
		wanted[citation.SourceID] = true
	}
	for id := range s.readSources {
		wanted[id] = true
	}
	for _, source := range fusion.Merge(s.sources, 0, 0) {
		if wanted[source.ID] && !ids[source.ID] {
			selected = append(selected, source)
			ids[source.ID] = true
		}
	}
	return selected
}

func (s *execution) retainCitedSources(selected []core.Source) []core.Source {
	ids := map[string]bool{}
	for _, source := range selected {
		ids[source.ID] = true
	}
	wanted := map[string]bool{}
	for _, citation := range s.citations {
		wanted[citation.SourceID] = true
	}
	for _, source := range fusion.Merge(s.sources, 0, 0) {
		if wanted[source.ID] && !ids[source.ID] {
			selected = append(selected, source)
			ids[source.ID] = true
		}
	}
	return selected
}
