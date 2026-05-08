package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/store"
)

func TestQueueRetryRequeuesFailedItemAndAudits(t *testing.T) {
	st := newQueueTestStore(t)
	ctx := context.Background()
	if _, err := st.RepoCreate(ctx, "repo01", "Repo", t.TempDir(), "master", "", "", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TaskCreate(ctx, "task01", "", "repo01", "Feature", "", "feature", "", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MergeQueueEnqueue(ctx, "mq01", "task01", "repo01", "clankwork/task01", "master", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.MergeQueueSetStatus(ctx, "mq01", "failed"); err != nil {
		t.Fatal(err)
	}
	if err := st.MergeQueueSetFailureLog(ctx, "mq01", "old verify failure"); err != nil {
		t.Fatal(err)
	}

	resp := postQueueRetry(t, NewServer(st, t.TempDir()), model.QueueSkipRequest{ItemID: "mq01"})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", resp.Code, resp.Body.String())
	}
	item, err := st.MergeQueueGet(ctx, "mq01")
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "queued" {
		t.Fatalf("status = %q, want queued", item.Status)
	}
	if item.FailureLog != "" {
		t.Fatalf("failure log = %q, want empty", item.FailureLog)
	}
	events, err := st.ControlPlaneEvents(ctx, "task01", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var sawRetry bool
	for _, ev := range events {
		if ev.Source == "actuation" && ev.Type == "queue.retry" && ev.TargetID == "mq01" {
			sawRetry = true
		}
	}
	if !sawRetry {
		t.Fatalf("queue.retry actuation not found in events: %+v", events)
	}
}

func TestQueueRetryRejectsActiveItem(t *testing.T) {
	st := newQueueTestStore(t)
	ctx := context.Background()
	if _, err := st.RepoCreate(ctx, "repo01", "Repo", t.TempDir(), "master", "", "", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TaskCreate(ctx, "task01", "", "repo01", "Feature", "", "feature", "", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MergeQueueEnqueue(ctx, "mq01", "task01", "repo01", "clankwork/task01", "master", 0); err != nil {
		t.Fatal(err)
	}

	resp := postQueueRetry(t, NewServer(st, t.TempDir()), model.QueueSkipRequest{ItemID: "mq01"})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", resp.Code, resp.Body.String())
	}
}

func newQueueTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func postQueueRetry(t *testing.T, s *Server, req model.QueueSkipRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/queue.retry", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleQueueRetry(w, r)
	return w
}
