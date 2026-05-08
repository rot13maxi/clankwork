package api

import (
	"net/http"
	"time"

	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/model"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	OK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	taskStats, err := s.store.TaskStats(ctx)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	agentStats, err := s.store.AgentStats(ctx)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	planTotal, planActive, _ := s.store.PlanStats(ctx)
	mergeStats, _ := s.store.MergeQueueStats(ctx)
	queueDecision := model.QueuePressureDecision{Level: model.QueuePressureNone, Reason: "merge queue within target"}
	if s.dispatcher != nil {
		queueDecision = s.dispatcher.QueuePressureDecision()
	} else if cfg, err := config.Load(s.homeDir); err == nil {
		snapshot, _ := s.store.MergeQueuePressureSnapshot(ctx, time.Now().Add(-1*time.Hour))
		queueDecision = model.ComputeQueuePressure(snapshot, cfg.Scheduler.MergeQueueMaxDepth, 30*time.Minute, cfg.Scheduler.MaxSlots)
	}

	OK(w, model.StatusResponse{
		Tasks:         taskStats,
		Agents:        agentStats,
		Plans:         model.PlanStats{Total: planTotal, Active: planActive},
		MergeQueue:    mergeStats,
		QueuePressure: queueDecision,
	})
}
