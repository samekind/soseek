package core

const DefaultSourceChars = 4000
const MaxSourceChars = 20000

// SourceWindow is a window of already stored evidence, never a new page fetch.
// Offsets and lengths count Unicode code points, not bytes or grapheme clusters.
type SourceWindow struct {
	RunID             string `json:"run_id"`
	SourceID          string `json:"source_id"`
	Title             string `json:"title,omitempty"`
	URL               string `json:"url"`
	PublishedAt       string `json:"published_at,omitempty"`
	Field             string `json:"field"`
	Text              string `json:"text"`
	Offset            int    `json:"offset"`
	ReturnedChars     int    `json:"returned_chars"`
	TotalChars        int    `json:"total_chars"`
	NextOffset        *int   `json:"next_offset,omitempty"`
	Truncated         bool   `json:"truncated"`
	UpstreamTruncated bool   `json:"upstream_truncated"`
}

func WindowSource(runID string, source Source, offset, maxChars int) (SourceWindow, error) {
	if maxChars == 0 {
		maxChars = DefaultSourceChars
	}
	if offset < 0 || maxChars < 1 || maxChars > MaxSourceChars {
		return SourceWindow{}, E("invalid_request", "offset must be nonnegative and max_chars must be between 1 and 20000")
	}
	field, content := "content", source.Content
	if content == "" {
		field, content = "snippet", source.Snippet
	}
	chars := []rune(content)
	if offset > len(chars) {
		return SourceWindow{}, E("invalid_request", "offset exceeds stored source length")
	}
	end := offset + min(maxChars, len(chars)-offset)
	w := SourceWindow{RunID: runID, SourceID: source.ID, Title: source.Title,
		URL: source.URL, PublishedAt: source.PublishedAt, Field: field,
		Text: string(chars[offset:end]), Offset: offset, ReturnedChars: end - offset,
		TotalChars: len(chars), Truncated: offset > 0 || end < len(chars),
		UpstreamTruncated: source.Truncated}
	if end < len(chars) {
		w.NextOffset = &end
	}
	return w, nil
}
