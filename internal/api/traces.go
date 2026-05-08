package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rot13maxi/clankwork/internal/model"
)

func (s *Server) handleTracesList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	taskID := q.Get("task_id")
	eventType := q.Get("type")
	sinceStr := q.Get("since")

	limit := 50
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	template := q.Get("template")
	retries := q.Get("retries")
	outcome := q.Get("outcome")
	pathGlob := q.Get("path")

	var since time.Time
	if sinceStr != "" {
		since = parseDuration(sinceStr)
	}

	traces, err := s.store.TraceListFiltered(r.Context(), taskID, eventType, since, limit, template, retries, outcome, pathGlob)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if traces == nil {
		traces = []*model.Trace{}
	}
	OK(w, traces)
}

// parseDuration parses a human duration like "7d", "24h", "30m" into a time.Time
// representing that duration ago from now.
func parseDuration(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}

	// Try standard Go duration first (e.g., "24h", "30m").
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d)
	}

	// Handle "Nd" format for days.
	if strings.HasSuffix(s, "d") {
		numStr := strings.TrimSuffix(s, "d")
		if n, err := strconv.Atoi(numStr); err == nil {
			return time.Now().Add(-time.Duration(n) * 24 * time.Hour)
		}
	}

	return time.Time{}
}
