package api

import (
	"net/http"

	"github.com/rot13maxi/clankwork/internal/mergequeue"
	"github.com/rot13maxi/clankwork/internal/scheduler"
	"github.com/rot13maxi/clankwork/internal/store"
	"github.com/rot13maxi/clankwork/internal/worker"
)

type Server struct {
	store          *store.Store
	homeDir        string
	dispatcher     *scheduler.Dispatcher
	worktree       worker.WorktreeCreator
	mergeProcessor *mergequeue.Processor
}

func NewServer(st *store.Store, homeDir string) *Server {
	return &Server{store: st, homeDir: homeDir}
}

func NewServerWithDispatcher(st *store.Store, homeDir string, d *scheduler.Dispatcher, wt worker.WorktreeCreator) *Server {
	return &Server{store: st, homeDir: homeDir, dispatcher: d, worktree: wt}
}

func (s *Server) SetMergeProcessor(p *mergequeue.Processor) {
	s.mergeProcessor = p
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health & status
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/status", s.handleStatus)

	// Repos
	mux.HandleFunc("POST /v1/repos.create", s.handleReposCreate)
	mux.HandleFunc("GET /v1/repos.get", s.handleReposGet)
	mux.HandleFunc("GET /v1/repos.list", s.handleReposList)

	// Plans
	mux.HandleFunc("POST /v1/plans.create", s.handlePlansCreate)
	mux.HandleFunc("GET /v1/plans.list", s.handlePlansList)
	mux.HandleFunc("GET /v1/plans.get", s.handlePlansGet)

	// Tasks
	mux.HandleFunc("POST /v1/tasks.create", s.handleTasksCreate)
	mux.HandleFunc("GET /v1/tasks.list", s.handleTasksList)
	mux.HandleFunc("GET /v1/tasks.get", s.handleTasksGet)
	mux.HandleFunc("GET /v1/tasks.getByName", s.handleTasksGetByName)
	mux.HandleFunc("POST /v1/tasks.addDep", s.handleTasksAddDep)
	mux.HandleFunc("POST /v1/tasks.setPriority", s.handleTasksSetPriority)
	mux.HandleFunc("POST /v1/tasks.retry", s.handleTasksRetry)
	mux.HandleFunc("POST /v1/tasks.close", s.handleTasksClose)
	mux.HandleFunc("GET /v1/tasks.diagnose", s.handleTasksDiagnose)
	mux.HandleFunc("POST /v1/tasks.retryStep", s.handleTasksRetryStep)
	mux.HandleFunc("POST /v1/tasks.resetStep", s.handleTasksResetStep)
	mux.HandleFunc("POST /v1/tasks.escalate", s.handleTasksEscalate)

	// Control-plane operators and views
	mux.HandleFunc("POST /v1/reconcile.task", s.handleReconcileTask)
	mux.HandleFunc("POST /v1/reconcile.all", s.handleReconcileAll)
	mux.HandleFunc("POST /v1/refresh.task", s.handleRefreshTask)
	mux.HandleFunc("POST /v1/refresh.agent", s.handleRefreshAgent)
	mux.HandleFunc("POST /v1/refresh.worktree", s.handleRefreshWorktree)
	mux.HandleFunc("GET /v1/events.list", s.handleEventsList)
	mux.HandleFunc("GET /v1/escalations.list", s.handleEscalationsList)
	mux.HandleFunc("POST /v1/escalations.resolve", s.handleEscalationsResolve)

	// Signals
	mux.HandleFunc("POST /v1/signals.started", s.handleSignal("signal.started", "running"))
	mux.HandleFunc("POST /v1/signals.progress", s.handleSignalProgress)
	mux.HandleFunc("POST /v1/signals.done", s.handleSignalDone)
	mux.HandleFunc("POST /v1/signals.failed", s.handleSignalFailed)
	mux.HandleFunc("POST /v1/signals.blocked", s.handleSignal("signal.blocked", "blocked"))

	// Acceptance artifacts
	mux.HandleFunc("GET /v1/acceptance.spec", s.handleAcceptanceSpecGet)
	mux.HandleFunc("POST /v1/acceptance.spec", s.handleAcceptanceSpecPut)
	mux.HandleFunc("GET /v1/acceptance.doneBundle", s.handleDoneBundleGet)
	mux.HandleFunc("GET /v1/acceptance.verificationReport", s.handleVerificationReportGet)
	mux.HandleFunc("POST /v1/artifacts.add", s.handleArtifactAdd)
	mux.HandleFunc("GET /v1/artifacts.list", s.handleArtifactsList)

	// Agents
	mux.HandleFunc("GET /v1/agents.list", s.handleAgentsList)
	mux.HandleFunc("GET /v1/agents.get", s.handleAgentsGet)
	mux.HandleFunc("GET /v1/agents.getByTask", s.handleAgentsGetByTask)
	mux.HandleFunc("GET /v1/agents.events", s.handleAgentEvents)
	mux.HandleFunc("GET /v1/agents.permissions", s.handleAgentPermissions)
	mux.HandleFunc("POST /v1/agents.send", s.handleAgentSend)
	mux.HandleFunc("POST /v1/agents.cancel", s.handleAgentCancel)
	mux.HandleFunc("POST /v1/agents.permissionDecision", s.handleAgentPermissionDecision)

	// Dispatch pause/resume
	mux.HandleFunc("POST /v1/dispatch.pause", s.handleDispatchPause)
	mux.HandleFunc("POST /v1/dispatch.resume", s.handleDispatchResume)
	mux.HandleFunc("GET /v1/dispatch.status", s.handleDispatchStatus)

	// Merge queue
	mux.HandleFunc("GET /v1/queue.list", s.handleQueueList)
	mux.HandleFunc("POST /v1/queue.skip", s.handleQueueSkip)
	mux.HandleFunc("POST /v1/queue.retry", s.handleQueueRetry)

	// Agent entry point
	mux.HandleFunc("POST /v1/bootstrap", s.handleBootstrap)
	mux.HandleFunc("POST /v1/context.get", s.handleContextGet)

	// Traces
	mux.HandleFunc("GET /v1/traces.list", s.handleTracesList)

	// Config
	mux.HandleFunc("GET /v1/config", s.handleConfigGet)

	// Learnings
	mux.HandleFunc("POST /v1/learnings.add", s.handleLearningsAdd)
	mux.HandleFunc("POST /v1/learnings.candidateAdd", s.handleCandidateLearningAdd)
	mux.HandleFunc("GET /v1/learnings.candidateList", s.handleCandidateLearningList)

	mux.HandleFunc("GET /prior-art/search", s.handlePriorArtSearch)
	mux.HandleFunc("GET /v1/prior-art.search", s.handlePriorArtSearch)
	mux.HandleFunc("GET /v1/prior-art.show", s.handlePriorArtShow)
	mux.HandleFunc("POST /v1/prior-art.rebuild", s.handlePriorArtRebuild)

	return mux
}
