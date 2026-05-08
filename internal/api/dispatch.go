package api

import (
	"net/http"
)

func (s *Server) handleDispatchPause(w http.ResponseWriter, r *http.Request) {
	if s.dispatcher == nil {
		Fail(w, http.StatusServiceUnavailable, "no_dispatcher", "dispatcher not configured")
		return
	}
	s.dispatcher.Pause()
	OK(w, map[string]bool{"paused": true})
}

func (s *Server) handleDispatchResume(w http.ResponseWriter, r *http.Request) {
	if s.dispatcher == nil {
		Fail(w, http.StatusServiceUnavailable, "no_dispatcher", "dispatcher not configured")
		return
	}
	s.dispatcher.Resume()
	OK(w, map[string]bool{"paused": false})
}

func (s *Server) handleDispatchStatus(w http.ResponseWriter, r *http.Request) {
	if s.dispatcher == nil {
		OK(w, map[string]bool{"paused": false})
		return
	}
	OK(w, map[string]bool{"paused": s.dispatcher.IsPaused()})
}
