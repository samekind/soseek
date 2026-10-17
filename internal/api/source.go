package api

import (
	"net/http"
	"strconv"

	"github.com/samekind/soseek/internal/core"
)

func (s *Server) source(w http.ResponseWriter, r *http.Request) {
	run, err := s.db.GetRun(r.Context(), principal(r).ID, r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	offset, limit := 0, core.DefaultSourceChars
	for key, dst := range map[string]*int{"offset": &offset, "max_chars": &limit} {
		if values, exists := r.URL.Query()[key]; exists {
			if len(values) != 1 || values[0] == "" {
				fail(w, core.E("invalid_request", key+" must be a single integer"))
				return
			}
			*dst, err = strconv.Atoi(values[0])
			if err != nil {
				fail(w, core.E("invalid_request", key+" must be an integer"))
				return
			}
		}
	}
	if offset < 0 || limit < 1 || limit > core.MaxSourceChars {
		fail(w, core.E("invalid_request", "offset must be nonnegative and max_chars must be between 1 and 20000"))
		return
	}
	if run.Result == nil {
		fail(w, core.E("conflict", "run result is not yet available"))
		return
	}
	for _, source := range run.Result.Results {
		if source.ID != r.PathValue("source") {
			continue
		}
		window, err := core.WindowSource(run.ID, source, offset, limit)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, http.StatusOK, window)
		return
	}
	fail(w, core.E("not_found", "source is not present in this run"))
}
