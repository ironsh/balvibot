// Package approvalserver runs the approval service: a small REST API the
// offline balvi-approve CLI uses to list and approve queued actions, plus the
// executor that dispatches an action once its approval is verified. Actions are
// enqueued elsewhere (the balvibot-api MCP server writes them to the DB);
// approvals are authenticated by SSH signature.
package approvalserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/ironsh/balvibot/tools/api/internal/approval"
	"github.com/ironsh/balvibot/tools/api/internal/store"
)

type Config struct {
	BindAddr string
}

// Run starts the approval server and blocks until ctx is cancelled or the
// listener fails. It shuts down gracefully on ctx.Done.
func Run(ctx context.Context, cfg Config, st *store.Store, registry *approval.Registry, logger *slog.Logger) error {
	if cfg.BindAddr == "" {
		return errors.New("approval bind address is required")
	}

	rh := &restHandlers{st: st, registry: registry, logger: logger}

	mux := http.NewServeMux()
	// Operator-facing REST: approve is gated by SSH signature; list/get are
	// reachable only over the node's SSH session (see SOUL.md).
	mux.HandleFunc("GET /actions", rh.listActions)
	mux.HandleFunc("GET /actions/{id}", rh.getAction)
	mux.HandleFunc("POST /actions/{id}/approve", rh.approveAction)
	mux.HandleFunc("GET /approval-users/{email}", rh.getApprovalUser)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	httpSrv := &http.Server{
		Addr:              cfg.BindAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("approval server listening", "addr", cfg.BindAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}
