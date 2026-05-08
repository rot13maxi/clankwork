package api

import (
	"net/http"

	"github.com/rot13maxi/clankwork/internal/model"
)

func (s *Server) handleQueueList(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.MergeQueueList(r.Context())
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if items == nil {
		items = []*model.MergeQueueItem{}
	}
	OK(w, items)
}

func (s *Server) handleQueueSkip(w http.ResponseWriter, r *http.Request) {
	var req model.QueueSkipRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.ItemID == "" {
		Fail(w, http.StatusBadRequest, "missing_id", "item_id required")
		return
	}
	item, err := s.store.MergeQueueGet(r.Context(), req.ItemID)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if item.Status != "queued" && item.Status != "conflicted" && item.Status != "failed" {
		Fail(w, http.StatusBadRequest, "not_skippable", "item is not in a skippable state: "+item.Status)
		return
	}
	if err := s.store.MergeQueueSetStatus(r.Context(), req.ItemID, "rejected"); err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	s.recordActuation(r, "queue.skip", defaultActor(""), "merge_item", item.ID, item.TaskID, "", "", item.Status, "rejected", "success", "", "operator rejected merge queue item")
	OK(w, map[string]string{"id": req.ItemID, "status": "rejected"})
}

func (s *Server) handleQueueRetry(w http.ResponseWriter, r *http.Request) {
	var req model.QueueSkipRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.ItemID == "" {
		Fail(w, http.StatusBadRequest, "missing_id", "item_id required")
		return
	}
	item, err := s.store.MergeQueueGet(r.Context(), req.ItemID)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	switch item.Status {
	case "failed", "rejected", "conflicted":
	default:
		Fail(w, http.StatusBadRequest, "not_retryable", "item is not in a retryable state: "+item.Status)
		return
	}
	if err := s.store.MergeQueueSetFailureLog(r.Context(), req.ItemID, ""); err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if err := s.store.MergeQueueSetStatus(r.Context(), req.ItemID, "queued"); err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	s.recordActuation(r, "queue.retry", defaultActor(""), "merge_item", item.ID, item.TaskID, "", "", item.Status, "queued", "success", "", "operator re-queued merge queue item")
	OK(w, map[string]string{"id": req.ItemID, "status": "queued"})
}
