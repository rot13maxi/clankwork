package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/rot13maxi/clankwork/internal/model"
)

func (s *Server) handleAgentsList(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	agents, err := s.store.AgentList(r.Context(), status)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if agents == nil {
		agents = nil // keep JSON null vs []
	}
	OK(w, agents)
}

func (s *Server) handleAgentsGet(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		Fail(w, http.StatusBadRequest, "missing_param", "id is required")
		return
	}
	agent, err := s.store.AgentGet(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			Fail(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	OK(w, agent)
}

func (s *Server) handleAgentsGetByTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		Fail(w, http.StatusBadRequest, "missing_param", "task_id is required")
		return
	}
	agent, err := s.store.AgentGetByTask(r.Context(), taskID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			Fail(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	OK(w, agent)
}

func (s *Server) handleAgentEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	agentID := q.Get("agent_id")
	taskID := q.Get("task_id")
	if agentID == "" && taskID == "" {
		Fail(w, http.StatusBadRequest, "missing_param", "agent_id or task_id is required")
		return
	}
	afterSeq := int64(0)
	if raw := q.Get("after_seq"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			Fail(w, http.StatusBadRequest, "invalid_param", "after_seq must be a non-negative integer")
			return
		}
		afterSeq = n
	}
	limit := 200
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			Fail(w, http.StatusBadRequest, "invalid_param", "limit must be a positive integer")
			return
		}
		limit = n
	}
	events, err := s.store.AgentEventsList(r.Context(), agentID, taskID, afterSeq, limit)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if events == nil {
		events = nil
	}
	OK(w, events)
}

func (s *Server) handleAgentSend(w http.ResponseWriter, r *http.Request) {
	if s.dispatcher == nil {
		Fail(w, http.StatusServiceUnavailable, "dispatcher_unavailable", "dispatcher is not configured")
		return
	}
	var req model.AgentSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Fail(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if req.AgentID == "" {
		Fail(w, http.StatusBadRequest, "missing_param", "agent_id is required")
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		Fail(w, http.StatusBadRequest, "missing_param", "message is required")
		return
	}
	if err := s.dispatcher.SendToAgent(r.Context(), req.AgentID, req.Message); err != nil {
		if strings.Contains(err.Error(), "active turn already exists") || strings.Contains(err.Error(), "begin turn failed") || strings.Contains(err.Error(), "turn/start failed") {
			Fail(w, http.StatusConflict, "agent_busy", "agent is currently busy; use `clankwork agents watch` to observe it or `clankwork agents cancel` before sending another message")
			return
		}
		Fail(w, http.StatusInternalServerError, "send_failed", err.Error())
		return
	}
	OK(w, map[string]bool{"sent": true})
}

func (s *Server) handleAgentPermissions(w http.ResponseWriter, r *http.Request) {
	if s.dispatcher == nil {
		Fail(w, http.StatusServiceUnavailable, "dispatcher_unavailable", "dispatcher is not configured")
		return
	}
	agentID := r.URL.Query().Get("agent_id")
	if agentID == "" {
		Fail(w, http.StatusBadRequest, "missing_param", "agent_id is required")
		return
	}
	pending, err := s.dispatcher.PendingAgentPermissions(r.Context(), agentID)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "permissions_failed", err.Error())
		return
	}
	out := make([]model.ACPPermissionRequest, 0, len(pending))
	for _, p := range pending {
		out = append(out, model.ACPPermissionRequest{
			ID:          p.ID,
			SessionName: p.SessionName,
			SessionID:   p.SessionID,
			Command:     p.Command,
			Policy:      p.Policy,
			Options:     p.Options,
			CreatedAt:   p.CreatedAt,
		})
	}
	OK(w, out)
}

func (s *Server) handleAgentPermissionDecision(w http.ResponseWriter, r *http.Request) {
	if s.dispatcher == nil {
		Fail(w, http.StatusServiceUnavailable, "dispatcher_unavailable", "dispatcher is not configured")
		return
	}
	var req model.AgentPermissionDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Fail(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if req.AgentID == "" || req.RequestID == "" || req.Decision == "" {
		Fail(w, http.StatusBadRequest, "missing_param", "agent_id, request_id, and decision are required")
		return
	}
	if err := s.dispatcher.ResolveAgentPermission(r.Context(), req.AgentID, req.RequestID, req.Decision); err != nil {
		Fail(w, http.StatusInternalServerError, "permission_decision_failed", err.Error())
		return
	}
	OK(w, map[string]bool{"resolved": true})
}

func (s *Server) handleAgentCancel(w http.ResponseWriter, r *http.Request) {
	if s.dispatcher == nil {
		Fail(w, http.StatusServiceUnavailable, "dispatcher_unavailable", "dispatcher is not configured")
		return
	}
	var req model.AgentSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Fail(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if req.AgentID == "" {
		Fail(w, http.StatusBadRequest, "missing_param", "agent_id is required")
		return
	}
	if err := s.dispatcher.CancelAgent(r.Context(), req.AgentID); err != nil {
		Fail(w, http.StatusInternalServerError, "cancel_failed", err.Error())
		return
	}
	OK(w, map[string]bool{"cancelled": true})
}
