package api

import (
	"net/http"

	"github.com/rot13maxi/clankwork/internal/config"
)

func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.homeDir)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "config_error", err.Error())
		return
	}
	OK(w, cfg)
}
