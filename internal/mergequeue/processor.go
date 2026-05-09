package mergequeue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/store"
	tmplpkg "github.com/rot13maxi/clankwork/internal/template"
)

// Pressurable is implemented by *scheduler.Dispatcher to set queue backpressure.
type Pressurable interface {
	SetQueuePressure(on bool)
	SetQueuePressureDecision(model.QueuePressureDecision)
}

type Processor struct {
	store      *store.Store
	cfg        *config.Config
	homeDir    string
	dispatcher Pressurable

	mu         sync.Mutex
	activeRepo map[string]bool // repos currently being processed

	lastQueuePressure       model.QueuePressureDecision
	hadQueuePressureHistory bool
}

func NewProcessor(st *store.Store, cfg *config.Config, homeDir string, disp Pressurable) *Processor {
	return &Processor{
		store:      st,
		cfg:        cfg,
		homeDir:    homeDir,
		dispatcher: disp,
		activeRepo: make(map[string]bool),
	}
}

func (p *Processor) Tick(ctx context.Context) error {
	// Update backpressure.
	snapshot, err := p.store.MergeQueuePressureSnapshot(ctx, time.Now().Add(-1*time.Hour))
	if err == nil && p.dispatcher != nil {
		decision := model.ComputeQueuePressure(snapshot, p.cfg.Scheduler.MergeQueueMaxDepth, 30*time.Minute, p.cfg.Scheduler.MaxSlots)
		p.dispatcher.SetQueuePressureDecision(decision)
		if p.shouldRecordQueuePressure(decision) {
			_ = p.store.ControlObservationPut(ctx, &model.ControlObservation{
				TargetType: "merge_queue",
				TargetID:   "global",
				Kind:       "queue_pressure",
				Status:     decision.Level,
				Reason:     decision.Reason,
				Payload:    model.MarshalPayload(decision),
			})
		}
	}

	repos, err := p.store.RepoList(ctx)
	if err != nil {
		return fmt.Errorf("repo list: %w", err)
	}

	for _, repo := range repos {
		p.mu.Lock()
		if p.activeRepo[repo.ID] {
			p.mu.Unlock()
			continue
		}
		item, err := p.store.MergeQueueNext(ctx, repo.ID)
		if err != nil || item == nil {
			p.mu.Unlock()
			continue
		}
		p.activeRepo[repo.ID] = true
		p.mu.Unlock()

		go func(r *model.Repo, i *model.MergeQueueItem) {
			defer func() {
				p.mu.Lock()
				delete(p.activeRepo, r.ID)
				p.mu.Unlock()
			}()
			if err := p.processItem(context.Background(), i, r); err != nil {
				slog.Error("merge queue process error", "item", i.ID, "err", err)
			}
		}(repo, item)
	}
	return nil
}

func (p *Processor) shouldRecordQueuePressure(decision model.QueuePressureDecision) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	changed := !p.hadQueuePressureHistory ||
		p.lastQueuePressure.Level != decision.Level ||
		p.lastQueuePressure.MaxDispatch != decision.MaxDispatch ||
		p.lastQueuePressure.ShouldPause != decision.ShouldPause ||
		p.lastQueuePressure.Reason != decision.Reason

	if changed {
		p.lastQueuePressure = decision
		p.hadQueuePressureHistory = true
	}

	return changed
}

func (p *Processor) processItem(ctx context.Context, item *model.MergeQueueItem, repo *model.Repo) error {
	slog.Info("merge queue: processing", "item", item.ID, "task", item.TaskID, "branch", item.Branch)
	p.recordDecision(ctx, item, "merge_attempt", "process_item", "queued merge item is ready for processing", true)

	timeout := time.Duration(p.cfg.Scheduler.VerifyTimeoutSec) * time.Second
	mergingDir := filepath.Join(p.homeDir, "merging")
	if err := os.MkdirAll(mergingDir, 0700); err != nil {
		return fmt.Errorf("mkdir merging: %w", err)
	}

	// Create temp worktree from the task branch.
	p.store.MergeQueueSetStatus(ctx, item.ID, "rebasing")
	worktreePath, err := createMergeWorktree(repo.Path, item.TaskID, item.Branch, p.homeDir)
	if err != nil {
		return p.failItem(ctx, item, fmt.Sprintf("create worktree: %v", err))
	}
	p.store.MergeQueueSetWorktreePath(ctx, item.ID, worktreePath)
	p.recordActuation(ctx, item, "merge.create_worktree", "rebasing", "rebasing", "success", "", "created merge worktree")

	// Fetch + rebase.
	result, err := fetchAndRebase(worktreePath, repo.Path, item.Target, timeout)
	if err != nil {
		removeMergeWorktree(repo.Path, worktreePath)
		return p.failItem(ctx, item, fmt.Sprintf("fetch/rebase error: %v", err))
	}

	if result.ConflictLog != "" {
		// Rebase conflicted. Spin up a conflict-resolution task.
		removeMergeWorktree(repo.Path, worktreePath)
		p.store.MergeQueueSetWorktreePath(ctx, item.ID, "")
		p.recordDecision(ctx, item, "merge_conflict", "classify_conflict", "rebase conflict requires classification", true)
		return p.handleConflict(ctx, item, repo, result.ConflictLog)
	}

	// Verify (if configured).
	if repo.VerifyCommand != "" {
		p.store.MergeQueueSetStatus(ctx, item.ID, "verifying")
		verifyOut, verifyErr := runVerify(worktreePath, repo.VerifyCommand, timeout)
		if verifyErr != nil {
			removeMergeWorktree(repo.Path, worktreePath)
			log := fmt.Sprintf("verify failed:\n%s\n%v", verifyOut, verifyErr)
			slog.Info("merge queue: verify failed, re-dispatching", "item", item.ID)
			p.store.MergeQueueSetFailureLog(ctx, item.ID, log)
			return p.requeueOrReject(ctx, item, repo, log)
		}
	}

	// Advance target branch (compare-and-swap).
	p.store.MergeQueueSetStatus(ctx, item.ID, "merging")
	p.recordDecision(ctx, item, "merge_ready", "advance_target", "verification passed; advancing target branch", false)
	if err := advanceTarget(repo.Path, item.Target, result.RebasedSHA, result.TargetSHA); err != nil {
		// Target advanced externally — re-queue for another rebase attempt.
		removeMergeWorktree(repo.Path, worktreePath)
		slog.Info("merge queue: target advanced externally, re-queuing", "item", item.ID)
		p.store.MergeQueueIncrAttempt(ctx, item.ID)
		p.store.MergeQueueSetStatus(ctx, item.ID, "queued")
		p.recordActuation(ctx, item, "merge.advance_target", "merging", "queued", "failed", err.Error(), "target branch advanced before compare-and-swap")
		return nil
	}
	p.recordActuation(ctx, item, "merge.advance_target", result.TargetSHA, result.RebasedSHA, "success", "", "target branch advanced")

	// Optional push.
	if repo.AutoPush {
		if pushErr := pushTarget(repo.Path, item.Target); pushErr != nil {
			log := fmt.Sprintf("push failed: %v", pushErr)
			slog.Error("merge queue: push failed (merge already local)", "item", item.ID, "err", pushErr)
			p.store.MergeQueueSetFailureLog(ctx, item.ID, log)
			p.recordActuation(ctx, item, "merge.push_target", "local", "origin", "failed", pushErr.Error(), "auto-push failed after local merge")
		}
	}

	// Cleanup.
	removeMergeWorktree(repo.Path, worktreePath)
	deleteBranch(repo.Path, item.Branch)

	// Mark merged.
	p.store.MergeQueueSetMergeSHA(ctx, item.ID, result.RebasedSHA)
	p.store.MergeQueueSetStatus(ctx, item.ID, "merged")
	p.store.TaskSetStatus(ctx, item.TaskID, "merged")
	p.recordActuation(ctx, item, "merge.complete", "merging", "merged", "success", "", "merge queue item completed")

	payload, _ := json.Marshal(map[string]string{"sha": result.RebasedSHA, "branch": item.Branch})
	p.store.TraceAppend(ctx, item.TaskID, "", "merge.merged", string(payload))
	slog.Info("merge queue: merged", "item", item.ID, "sha", result.RebasedSHA)
	return nil
}

func (p *Processor) handleConflict(ctx context.Context, item *model.MergeQueueItem, repo *model.Repo, conflictLog string) error {
	// Classify the conflict before deciding how to handle it.
	analysis := ClassifyConflict(conflictLog)

	classPayload, _ := json.Marshal(map[string]string{
		"class":  string(analysis.Class),
		"reason": analysis.Reason,
	})
	p.store.TraceAppend(ctx, item.TaskID, "", "merge.conflict_classified", string(classPayload))
	slog.Info("merge queue: conflict classified",
		"item", item.ID, "class", analysis.Class, "reason", analysis.Reason,
		"files", analysis.Files)

	// Semantic conflicts: reject immediately and re-dispatch the original task
	// for rework. No point spawning a conflict-resolver — the changes are
	// behaviorally contradictory and need human/agent judgment at the task level.
	if analysis.Class == ConflictSemantic {
		slog.Warn("merge queue: semantic conflict, rejecting for rework",
			"item", item.ID, "reason", analysis.Reason)
		p.store.MergeQueueSetFailureLog(ctx, item.ID,
			fmt.Sprintf("semantic conflict: %s\n\n%s", analysis.Reason, conflictLog))
		p.store.MergeQueueSetStatus(ctx, item.ID, "rejected")
		// Re-dispatch the original task for rework with the conflict context.
		p.store.TaskSetStatus(ctx, item.TaskID, "pending")

		payload, _ := json.Marshal(map[string]string{
			"class":  string(analysis.Class),
			"reason": analysis.Reason,
		})
		p.store.TraceAppend(ctx, item.TaskID, "", "merge.semantic_conflict_rejected", string(payload))
		return nil
	}

	// Trivial (mechanical) conflicts: spawn a conflict-resolver agent.
	p.store.MergeQueueIncrAttempt(ctx, item.ID)
	updated, _ := p.store.MergeQueueGet(ctx, item.ID)
	maxAttempts := p.cfg.Scheduler.MergeQueueMaxAttempts
	if updated != nil && updated.AttemptCount >= maxAttempts {
		slog.Warn("merge queue: max attempts on trivial conflict, rejecting", "item", item.ID)
		p.store.MergeQueueSetFailureLog(ctx, item.ID, conflictLog)
		p.store.MergeQueueSetStatus(ctx, item.ID, "rejected")
		return nil
	}

	// Build a body that includes the classification analysis for the resolver agent.
	analysisJSON, _ := json.Marshal(analysis)
	conflictTaskID := ulid.Make().String()
	body := fmt.Sprintf("Resolve merge conflicts for branch %s onto %s.\n\n"+
		"Conflict classification: %s (trivial — mechanical, safe to auto-resolve)\n"+
		"Reason: %s\n\n"+
		"Analysis:\n%s\n\n"+
		"Conflict log:\n%s",
		item.Branch, item.Target,
		analysis.Class, analysis.Reason,
		string(analysisJSON), conflictLog)
	conflictTask, err := p.store.TaskCreate(ctx,
		conflictTaskID, "", repo.ID,
		"Resolve conflicts: "+item.Branch,
		body, "", "conflict-resolver", "", item.Priority)
	if err != nil {
		return fmt.Errorf("create conflict task: %w", err)
	}

	p.store.MergeQueueSetConflictTask(ctx, item.ID, conflictTask.ID)
	p.store.MergeQueueSetFailureLog(ctx, item.ID, conflictLog)
	p.store.MergeQueueSetStatus(ctx, item.ID, "conflicted")

	payload, _ := json.Marshal(map[string]string{"conflict_task_id": conflictTask.ID})
	p.store.TraceAppend(ctx, item.TaskID, "", "merge.conflicted", string(payload))
	slog.Info("merge queue: trivial conflict, spawned resolver task",
		"item", item.ID, "conflict_task", conflictTask.ID)
	return nil
}

func (p *Processor) requeueOrReject(ctx context.Context, item *model.MergeQueueItem, repo *model.Repo, log string) error {
	p.store.MergeQueueIncrAttempt(ctx, item.ID)
	// Re-read attempt count after increment.
	updated, _ := p.store.MergeQueueGet(ctx, item.ID)
	maxAttempts := p.cfg.Scheduler.MergeQueueMaxAttempts
	if updated != nil && updated.AttemptCount >= maxAttempts {
		slog.Warn("merge queue: max attempts on verify failure, rejecting", "item", item.ID)
		p.store.MergeQueueSetStatus(ctx, item.ID, "rejected")
		p.recordDecision(ctx, item, "merge_verify_failed", "reject_item", "verification failed and retry budget is exhausted", false)
		p.recordActuation(ctx, item, "merge.verify", "verifying", "rejected", "failed", log, "merge verification failed")
		return nil
	}

	// Re-dispatch the original task for rework.
	p.store.TaskSetStatus(ctx, item.TaskID, "pending")
	p.store.MergeQueueSetStatus(ctx, item.ID, "failed")
	p.recordDecision(ctx, item, "merge_verify_failed", "redispatch_task", "verification failed; task returned to pending for rework", true)
	p.recordActuation(ctx, item, "merge.verify", "verifying", "failed", "failed", log, "merge verification failed")
	payload, _ := json.Marshal(map[string]string{"reason": "verify_failed", "log": log})
	p.store.TraceAppend(ctx, item.TaskID, "", "merge.verify_failed", string(payload))
	return nil
}

func (p *Processor) failItem(ctx context.Context, item *model.MergeQueueItem, reason string) error {
	p.store.MergeQueueSetFailureLog(ctx, item.ID, reason)

	// Re-queue with backoff if attempts remain; only use terminal 'failed' when exhausted.
	p.store.MergeQueueIncrAttempt(ctx, item.ID)
	updated, _ := p.store.MergeQueueGet(ctx, item.ID)
	maxAttempts := p.cfg.Scheduler.MergeQueueMaxAttempts
	if updated != nil && updated.AttemptCount < maxAttempts {
		slog.Warn("merge queue item re-queued after failure (attempts remain)",
			"item", item.ID, "attempt", updated.AttemptCount, "max", maxAttempts, "reason", reason)
		p.store.MergeQueueSetStatus(ctx, item.ID, "queued")
		return nil
	}

	slog.Error("merge queue item failed (max attempts exhausted)", "item", item.ID, "reason", reason)
	p.store.MergeQueueSetStatus(ctx, item.ID, "failed")
	p.recordDecision(ctx, item, "merge_processing_failed", "mark_failed", "merge processing failed and retry budget is exhausted", false)
	p.recordActuation(ctx, item, "merge.process", item.Status, "failed", "failed", reason, "merge processing failed")
	return nil
}

func (p *Processor) recordDecision(ctx context.Context, item *model.MergeQueueItem, kind, action, reason string, retryable bool) {
	_ = p.store.ReconcilerDecisionAppend(ctx, &model.ReconcilerDecision{
		Controller:   "merge_controller",
		TaskID:       item.TaskID,
		TargetType:   "merge_item",
		TargetID:     item.ID,
		DecisionKind: kind,
		Action:       action,
		Reason:       reason,
		Retryable:    retryable,
	})
}

func (p *Processor) recordActuation(ctx context.Context, item *model.MergeQueueItem, operation, previousState, newState, outcome, errText, reason string) {
	_ = p.store.ControllerActuationAppend(ctx, &model.ControllerActuation{
		RequestedOperation: operation,
		ActorType:          "controller",
		ActorID:            "merge_controller",
		TargetType:         "merge_item",
		TargetID:           item.ID,
		TaskID:             item.TaskID,
		PreviousState:      previousState,
		NewState:           newState,
		Outcome:            outcome,
		Error:              errText,
		Reason:             reason,
	})
}

// HandleConflictFailed resets the parent merge item when a conflict-resolver task fails.
// It removes the stale resolver worktree and re-queues the parent for a fresh attempt.
func (p *Processor) HandleConflictFailed(ctx context.Context, taskID string) {
	task, err := p.store.TaskGet(ctx, taskID)
	if err != nil || task.Status != "failed" {
		return
	}

	// Check if this task is a conflict-resolver (has an owning merge queue item).
	mqItem, err := p.store.MergeQueueGetByConflictTask(ctx, taskID)
	if err != nil || mqItem == nil || mqItem.Status != "conflicted" {
		return
	}

	// Remove the conflict-resolver's worktree so the next re-queue gets a clean checkout.
	resolverWorktree := filepath.Join(p.homeDir, "worktrees", taskID)
	if _, err := os.Stat(resolverWorktree); err == nil {
		repo, _ := p.store.RepoGet(ctx, mqItem.RepoID)
		if repo != nil {
			removeMergeWorktree(repo.Path, resolverWorktree)
		}
	}

	p.store.MergeQueueSetStatus(ctx, mqItem.ID, "queued")
	p.recordDecision(ctx, mqItem, "merge_conflict_resolver_failed", "requeue_parent", "conflict resolver failed; re-queueing parent merge item", true)
	p.recordActuation(ctx, mqItem, "merge.conflict_resolver", "conflicted", "queued", "success", "", "conflict resolver failed; retrying merge item")
	evtPayload, _ := json.Marshal(map[string]string{"conflict_task_id": taskID, "reason": "resolver_failed"})
	p.store.TraceAppend(ctx, mqItem.TaskID, "", "merge.conflict_resolver_failed", string(evtPayload))
	slog.Info("conflict resolver failed, re-queuing merge item", "item", mqItem.ID, "conflict_task", taskID)
}

// StartupRecovery resets stuck items to 'queued' and enqueues stranded done tasks.
// Called once at daemon startup before the processor goroutine starts.
// EnqueueIfAutoMerge checks whether taskID should be enqueued in the merge queue
// (auto_merge=true template, task is "done") and enqueues it if so. Safe to call
// multiple times: the store INSERT will fail silently on duplicate task_id.
// Also re-queues a parent merge item when taskID is a conflict-resolver task.
func (p *Processor) EnqueueIfAutoMerge(ctx context.Context, taskID string) {
	task, err := p.store.TaskGet(ctx, taskID)
	if err != nil || task.Status != "done" {
		return
	}

	// Conflict-resolver completion: re-queue the parent merge item.
	mqItem, err := p.store.MergeQueueGetByConflictTask(ctx, taskID)
	if err == nil && mqItem != nil && mqItem.Status == "conflicted" {
		// Remove the conflict-resolver's worktree so the next merge attempt
		// doesn't hit "branch already in use" on git worktree add.
		resolverWorktree := filepath.Join(p.homeDir, "worktrees", taskID)
		if _, statErr := os.Stat(resolverWorktree); statErr == nil {
			repo, _ := p.store.RepoGet(ctx, mqItem.RepoID)
			if repo != nil {
				removeMergeWorktree(repo.Path, resolverWorktree)
			}
		}

		p.store.MergeQueueSetStatus(ctx, mqItem.ID, "queued")
		evtPayload, _ := json.Marshal(map[string]string{"conflict_task_id": taskID})
		p.store.TraceAppend(ctx, mqItem.TaskID, "", "merge.conflict_resolved", string(evtPayload))
		slog.Info("conflict resolved, re-queuing merge item", "item", mqItem.ID, "conflict_task", taskID)
		return
	}

	if task.RepoID == "" || task.Template == "" {
		return
	}
	tmpl, err := tmplpkg.Load(task.Template, "", p.homeDir)
	if err != nil || !tmpl.AutoMerge {
		return
	}
	repo, err := p.store.RepoGet(ctx, task.RepoID)
	if err != nil {
		slog.Error("merge enqueue: repo not found", "task", taskID, "repo", task.RepoID, "err", err)
		return
	}
	target := repo.TargetBranch
	if target == "" {
		target = "main"
	}
	branch := "clankwork/" + task.ID
	id := ulid.Make().String()
	if _, err := p.store.MergeQueueEnqueue(ctx, id, task.ID, task.RepoID, branch, target, task.Priority); err != nil {
		slog.Error("merge enqueue failed", "task", taskID, "err", err)
	} else {
		slog.Info("merge enqueued", "task", taskID, "item", id, "branch", branch, "target", target)
	}
}

func (p *Processor) StartupRecovery(ctx context.Context) error {
	repos, err := p.store.RepoList(ctx)
	if err != nil {
		return err
	}
	repoByID := make(map[string]*model.Repo, len(repos))
	for _, r := range repos {
		repoByID[r.ID] = r
	}

	// Reset stuck in-progress items.
	stuck, err := p.store.MergeQueueResetStuck(ctx)
	if err != nil {
		return fmt.Errorf("reset stuck: %w", err)
	}
	for _, item := range stuck {
		// If the branch is already an ancestor of target, it was already merged.
		repo := repoByID[item.RepoID]
		if repo != nil && isMergedInto(repo.Path, item.Branch, item.Target) {
			slog.Info("startup: item already merged, marking merged", "item", item.ID)
			p.store.MergeQueueSetStatus(ctx, item.ID, "merged")
			p.store.TaskSetStatus(ctx, item.TaskID, "merged")
		} else {
			slog.Info("startup: resetting stuck item to queued", "item", item.ID)
			// Clean up stale worktree if it exists.
			if item.WorktreePath != "" {
				if repo != nil {
					removeMergeWorktree(repo.Path, item.WorktreePath)
				}
				p.store.MergeQueueSetWorktreePath(ctx, item.ID, "")
			}
			p.store.MergeQueueSetStatus(ctx, item.ID, "queued")
		}
	}

	// Enqueue stranded done tasks (missed signal between done and enqueue).
	stranded, err := p.store.MergeQueueFindStrandedDone(ctx)
	if err != nil {
		return fmt.Errorf("find stranded: %w", err)
	}
	for _, task := range stranded {
		if task.RepoID == "" || task.Template == "" {
			continue
		}
		tmpl, err := tmplpkg.Load(task.Template, "", "")
		if err != nil || !tmpl.AutoMerge {
			continue
		}
		repo := repoByID[task.RepoID]
		if repo == nil {
			continue
		}
		target := repo.TargetBranch
		if target == "" {
			target = "main"
		}
		branch := "clankwork/" + task.ID
		id := ulid.Make().String()
		if _, err := p.store.MergeQueueEnqueue(ctx, id, task.ID, task.RepoID, branch, target, task.Priority); err != nil {
			slog.Warn("startup: failed to enqueue stranded task", "task", task.ID, "err", err)
		} else {
			slog.Info("startup: enqueued stranded done task", "task", task.ID)
		}
	}

	// Reset conflicted items whose resolver task is in a terminal state.
	// If the resolver was killed during restart, the item would stay conflicted forever.
	conflicted, err := p.store.MergeQueueFindStuckConflicted(ctx)
	if err != nil {
		return fmt.Errorf("find stuck conflicted: %w", err)
	}
	for _, item := range conflicted {
		if item.ConflictTaskID == "" {
			continue
		}
		resolver, _ := p.store.TaskGet(ctx, item.ConflictTaskID)
		if resolver == nil {
			continue
		}
		isTerminal := resolver.Status == "failed" || resolver.Status == "done" || resolver.Status == "merged"
		if !isTerminal {
			continue
		}
		slog.Info("startup: resetting stuck conflicted item (resolver terminal)",
			"item", item.ID, "conflict_task", item.ConflictTaskID, "resolver_status", resolver.Status)

		// Remove stale resolver worktree.
		resolverWorktree := filepath.Join(p.homeDir, "worktrees", item.ConflictTaskID)
		if _, statErr := os.Stat(resolverWorktree); statErr == nil {
			repo := repoByID[item.RepoID]
			if repo != nil {
				removeMergeWorktree(repo.Path, resolverWorktree)
			}
		}

		p.store.MergeQueueSetStatus(ctx, item.ID, "queued")
	}

	// Prune stale worktree metadata from all repos.
	for _, repo := range repos {
		pruneWorktrees(repo.Path)
	}

	return nil
}
