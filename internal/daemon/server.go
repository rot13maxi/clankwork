package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/rot13maxi/clankwork/internal/api"
	"github.com/rot13maxi/clankwork/internal/mergequeue"
	"github.com/rot13maxi/clankwork/internal/scheduler"
	"github.com/rot13maxi/clankwork/internal/store"
	"github.com/rot13maxi/clankwork/internal/worker"
)

type Server struct {
	homeDir    string
	socketPath string
	store      *store.Store
	apiServer  *api.Server
	httpServer *http.Server
	listener   net.Listener
}

func New(homeDir, socketPath string, st *store.Store) *Server {
	apiServer := api.NewServer(st, homeDir)
	return &Server{
		homeDir:    homeDir,
		socketPath: socketPath,
		store:      st,
		apiServer:  apiServer,
	}
}

func NewWithDispatcher(homeDir, socketPath string, st *store.Store, d *scheduler.Dispatcher, wt worker.WorktreeCreator) *Server {
	apiServer := api.NewServerWithDispatcher(st, homeDir, d, wt)
	return &Server{
		homeDir:    homeDir,
		socketPath: socketPath,
		store:      st,
		apiServer:  apiServer,
	}
}

func (s *Server) SetMergeProcessor(p *mergequeue.Processor) {
	s.apiServer.SetMergeProcessor(p)
}

func (s *Server) Start() error {
	// Build handler here so SetMergeProcessor calls take effect before serving.
	s.httpServer = &http.Server{Handler: s.apiServer.Handler()}

	// Remove stale socket if it exists.
	os.Remove(s.socketPath)

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen on socket: %w", err)
	}
	if err := os.Chmod(s.socketPath, 0600); err != nil {
		ln.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}
	s.listener = ln

	slog.Info("daemon ready", "home", s.homeDir, "socket", s.socketPath)

	go func() {
		if err := s.httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
		}
	}()
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}
