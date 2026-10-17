package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/samekind/soseek/internal/core"
)

// Source reads a Unicode window from saved evidence. The compatibility fallback
// reads the existing result on older servers; neither path submits a new run.
func (c *Client) Source(ctx context.Context, runID, sourceID string, offset, maxChars int) (core.SourceWindow, error) {
	var out core.SourceWindow
	if maxChars == 0 {
		maxChars = core.DefaultSourceChars
	}
	if offset < 0 || maxChars < 1 || maxChars > core.MaxSourceChars || sourceID == "" {
		return out, core.E("invalid_request", "source_id is required; offset must be nonnegative and max_chars between 1 and 20000")
	}
	path := RunPath(runID) + "/sources/" + url.PathEscape(sourceID) + "?offset=" + strconv.Itoa(offset) + "&max_chars=" + strconv.Itoa(maxChars)
	err := c.Do(ctx, http.MethodGet, path, nil, &out, nil)
	if err == nil {
		if out.RunID != runID || out.SourceID != sourceID {
			return core.SourceWindow{}, errors.New("cloud returned a mismatched source window")
		}
		return out, nil
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		return out, err
	}
	result, err := c.Result(ctx, runID)
	if err != nil {
		return out, err
	}
	if !core.Terminal(result.Status) {
		return out, core.E("conflict", "run result is not yet available")
	}
	if result.RunID != runID {
		return out, errors.New("cloud returned a mismatched run_id")
	}
	for _, source := range result.Results {
		if source.ID == sourceID {
			return core.WindowSource(runID, source, offset, maxChars)
		}
	}
	return out, core.E("not_found", "source is not present in this run")
}
