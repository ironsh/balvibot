// Package mcpserver exposes the indexer's docs corpus over a Streamable HTTP
// MCP endpoint. Read-only: there are no tools that mutate state, and no tool
// reads `unregistered_docs` or `blocked_owners` (those belong to the
// out-of-scope shadow-inbox MCP).
package mcpserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ironcd/philanthropy-os/tools/gdocs-indexer/internal/store"
)

const (
	serverName    = "gdocs-indexer"
	serverVersion = "0.1.0"
)

type Config struct {
	BindAddr    string
	BearerToken string
}

// Run starts the MCP server and blocks until ctx is cancelled or the
// listener fails.
func Run(ctx context.Context, cfg Config, st *store.Store, logger *slog.Logger) error {
	if cfg.BearerToken == "" {
		return errors.New("mcp bearer token is required")
	}
	if cfg.BindAddr == "" {
		return errors.New("mcp bind address is required")
	}

	mcpSrv := buildServer(st)

	streamHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return mcpSrv
	}, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", streamHandler)
	mux.Handle("/mcp/", streamHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	authed := bearerAuth(cfg.BearerToken, mux)

	httpSrv := &http.Server{
		Addr:              cfg.BindAddr,
		Handler:           authed,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("mcp server listening", "addr", cfg.BindAddr)
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

func bearerAuth(token string, next http.Handler) http.Handler {
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="gdocs-indexer"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// BuildServer is exported so tests can attach the MCP server to an
// httptest.Server without bringing up a real listener.
func BuildServer(st *store.Store) *mcp.Server {
	return buildServer(st)
}

func buildServer(st *store.Store) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}, nil)

	h := &handlers{st: st}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_grantees",
		Description: "List all grantees in the corpus with the number of documents the indexer holds for each one.",
	}, h.listGrantees)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_documents_for_grantee",
		Description: "Page through document summaries for a grantee. Returns title, modified time, sync time, and content flags. Use the doc_id with get_document_for_grantee to fetch the full markdown.",
	}, h.listDocumentsForGrantee)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_document_for_grantee",
		Description: "Return the full markdown content of a single document. Fails with document_not_found if the doc_id does not belong to the given grantee_id (defense against id guessing).",
	}, h.getDocumentForGrantee)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_document_metadata",
		Description: "Return everything get_document_for_grantee returns except the markdown body. Useful for cheap relevance checks before fetching content.",
	}, h.getDocumentMetadata)

	return s
}

type handlers struct {
	st *store.Store
}

func (h *handlers) listGrantees(ctx context.Context, _ *mcp.CallToolRequest, _ *ListGranteesInput) (*mcp.CallToolResult, *ListGranteesOutput, error) {
	gs, err := h.st.GranteeSummaries(ctx)
	if err != nil {
		return nil, nil, err
	}
	if gs == nil {
		gs = []store.GranteeSummary{}
	}
	return nil, &ListGranteesOutput{Grantees: gs}, nil
}

func (h *handlers) listDocumentsForGrantee(ctx context.Context, _ *mcp.CallToolRequest, in *ListDocumentsForGranteeInput) (*mcp.CallToolResult, *ListDocumentsForGranteeOutput, error) {
	if in.GranteeID == "" {
		return nil, nil, errors.New("grantee_id is required")
	}
	if _, err := h.st.GetGrantee(ctx, in.GranteeID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, fmt.Errorf("grantee_not_found: %s", in.GranteeID)
		}
		return nil, nil, err
	}
	docs, next, err := h.st.ListDocsForGrantee(ctx, in.GranteeID, in.Limit, in.Cursor)
	if err != nil {
		return nil, nil, err
	}
	if docs == nil {
		docs = []store.DocSummary{}
	}
	return nil, &ListDocumentsForGranteeOutput{Documents: docs, NextCursor: next}, nil
}

func (h *handlers) getDocumentForGrantee(ctx context.Context, _ *mcp.CallToolRequest, in *GetDocumentInput) (*mcp.CallToolResult, *GetDocumentOutput, error) {
	if in.GranteeID == "" || in.DocID == "" {
		return nil, nil, errors.New("grantee_id and doc_id are required")
	}
	d, err := h.st.GetDocForGrantee(ctx, in.GranteeID, in.DocID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, fmt.Errorf("document_not_found: %s for grantee %s", in.DocID, in.GranteeID)
		}
		return nil, nil, err
	}
	return nil, &GetDocumentOutput{Document: &DocumentDetail{
		DocID:           d.DocID,
		GranteeID:       d.GranteeID,
		Title:           d.Title,
		ContentMarkdown: d.ContentMarkdown,
		ModifiedAt:      d.ModifiedAt,
		SyncedAt:        d.SyncedAt,
		HadImages:       d.HadImages,
		HadComments:     d.HadComments,
		Status:          d.Status,
		Stale:           d.Status == store.StatusStale,
		LastError:       d.LastError,
	}}, nil
}

func (h *handlers) getDocumentMetadata(ctx context.Context, _ *mcp.CallToolRequest, in *GetDocumentInput) (*mcp.CallToolResult, *GetDocumentMetadataOutput, error) {
	if in.GranteeID == "" || in.DocID == "" {
		return nil, nil, errors.New("grantee_id and doc_id are required")
	}
	d, err := h.st.GetDocForGrantee(ctx, in.GranteeID, in.DocID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, fmt.Errorf("document_not_found: %s for grantee %s", in.DocID, in.GranteeID)
		}
		return nil, nil, err
	}
	return nil, &GetDocumentMetadataOutput{Document: &DocumentMetadata{
		DocID:       d.DocID,
		GranteeID:   d.GranteeID,
		Title:       d.Title,
		ModifiedAt:  d.ModifiedAt,
		SyncedAt:    d.SyncedAt,
		HadImages:   d.HadImages,
		HadComments: d.HadComments,
		Status:      d.Status,
		Stale:       d.Status == store.StatusStale,
		LastError:   d.LastError,
	}}, nil
}
