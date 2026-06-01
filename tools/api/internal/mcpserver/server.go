// Package mcpserver exposes the unified corpus (grantees, mail, docs) over a
// single Streamable HTTP MCP endpoint. The corpus read tools are read-only.
// The mutating tools never touch the corpus directly: they queue a
// side-effecting action into approval_actions for out-of-band human approval
// (executed later by the separate approval service), so the agent never acts
// without sign-off.
package mcpserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ironsh/balvibot/tools/api/internal/actions"
	"github.com/ironsh/balvibot/tools/api/internal/approval"
	"github.com/ironsh/balvibot/tools/api/internal/drive"
	"github.com/ironsh/balvibot/tools/api/internal/store"
)

// DriveLookup is the slice of the Drive client the whitelist_doc tool needs: a
// single metadata fetch used to classify an id as a folder or a doc at enqueue
// time. It may be nil, in which case whitelist_doc returns an error.
type DriveLookup interface {
	GetFile(ctx context.Context, fileID, fields string) (*drive.File, error)
}

const (
	serverName    = "philos-api"
	serverVersion = "0.1.0"
)

// serverInstructions is sent to clients during MCP initialization. It explains
// the read-only corpus tools and, importantly, the write-for-approval flow: the
// mutating tools never act immediately, they return an approval_id that a human
// operator approves out-of-band with the balvi-approve CLI.
const serverInstructions = `This server exposes a read-only corpus (grantees, mail, documents) plus a few mutating tools that are gated on human approval.

Read tools (list_grantees, list/get/search emails and threads, list/get documents) return data directly.

Mutating tools (add_grantee, add_approval_user, whitelist_doc) do NOT take effect immediately. Each one queues the action for human approval and returns an approval_id (with status "pending"). The change is applied only after an operator approves that approval_id out-of-band using the balvi-approve CLI, which signs the request with their SSH key. When you call a mutating tool, report the returned approval_id back to the user and tell them it must be approved via balvi-approve before it takes effect; do not assume the action has been performed.`

type Config struct {
	BindAddr    string
	BearerToken string
}

// Run starts the MCP server and blocks until ctx is cancelled or the listener
// returns an error. It shuts the HTTP server down gracefully on ctx.Done.
func Run(ctx context.Context, cfg Config, st *store.Store, drv DriveLookup, logger *slog.Logger) error {
	if cfg.BearerToken == "" {
		return errors.New("mcp bearer token is required")
	}
	if cfg.BindAddr == "" {
		return errors.New("mcp bind address is required")
	}

	mcpSrv := buildServer(st, drv)
	mcpSrv.AddReceivingMiddleware(logToolCalls(logger))

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
			w.Header().Set("WWW-Authenticate", `Bearer realm="philos-api"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// maxLoggedArgsLen bounds how much of an incoming tool call's raw arguments we
// write to the log, so a large body (e.g. a doc id list) can't blow up a line.
const maxLoggedArgsLen = 256

// logToolCalls is a receiving middleware that logs every incoming tools/call
// with the tool name and its truncated raw arguments. Other MCP methods
// (initialize, tools/list, ...) pass through unlogged.
func logToolCalls(logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if call, ok := req.(*mcp.CallToolRequest); ok && call.Params != nil {
				logger.Info("mcp tool call",
					"tool", call.Params.Name,
					"args", truncateArgs(call.Params.Arguments))
			}
			return next(ctx, method, req)
		}
	}
}

// truncateArgs renders raw JSON arguments as a single-line string capped at
// maxLoggedArgsLen, appending an ellipsis when the value is clipped.
func truncateArgs(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if len(raw) > maxLoggedArgsLen {
		return string(raw[:maxLoggedArgsLen]) + "…"
	}
	return string(raw)
}

// BuildServer is exported so tests can attach the MCP server to an
// httptest.Server without bringing up a real listener.
func BuildServer(st *store.Store, drv DriveLookup) *mcp.Server {
	return buildServer(st, drv)
}

func buildServer(st *store.Store, drv DriveLookup) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}, &mcp.ServerOptions{Instructions: serverInstructions})

	h := &handlers{st: st, drv: drv}

	// ---- grantees ----
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_grantees",
		Description: "List all grantees with their associated email addresses, status, and the number of documents held for each. Use the returned grantee_id with the other tools.",
	}, h.listGrantees)

	// ---- mail ----
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_emails_for_grantee",
		Description: "List email message summaries (most recent first) for a given grantee. Supports date range filtering and cursor pagination.",
	}, h.listEmailsForGrantee)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_email",
		Description: "Retrieve a single email message in full (headers, recipients, plain-text and HTML bodies, attachment metadata). Accepts either the numeric internal id or the RFC 5322 Message-ID.",
	}, h.getEmail)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_threads_for_grantee",
		Description: "List email threads (most recently active first) for a given grantee.",
	}, h.listThreadsForGrantee)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_thread",
		Description: "Retrieve a thread's metadata and all messages in chronological order.",
	}, h.getThread)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_emails",
		Description: "Search messages by any combination of grantee, From substring, Subject substring, Body substring, and date range. At least one filter is required.",
	}, h.searchEmails)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_attachments",
		Description: "List attachment metadata for a message (no file contents). Use the numeric message id from list_emails_for_grantee.",
	}, h.listAttachments)

	// ---- actions (write-for-approval) ----
	// Each mutating tool does NOT act immediately: it writes a pending row to
	// the approval queue and returns an approval_id. An operator approves it
	// out-of-band with balvi-approve, and only then does the executor run it.
	mcp.AddTool(s, &mcp.Tool{
		Name:        "add_grantee",
		Description: "Request creation of a new grantee. This does NOT create the grantee immediately: it queues the action for human approval and returns an approval_id. The grantee is created only after an operator approves it.",
	}, h.addGrantee)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "add_approval_user",
		Description: "Request authorization of a new approval operator by email and SSH public key. This does NOT authorize the operator immediately: it queues the action for human approval and returns an approval_id. The operator can approve actions only after an existing operator approves this request.",
	}, h.addApprovalUser)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "whitelist_doc",
		Description: "Request that a Google Drive doc or folder be ingested (indexed) for an existing grantee. Pass the grantee_id and the Drive id; whether it is a folder or a single doc is detected automatically. This does NOT index it immediately: it queues the action for human approval and returns an approval_id. Indexing begins only after an operator approves it.",
	}, h.whitelistDoc)

	// ---- docs ----
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
	st  *store.Store
	drv DriveLookup
}

func parseTimeBound(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return &t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, fmt.Errorf("invalid timestamp %q: expected RFC 3339", s)
	}
	return &t, nil
}

// ---------- actions (write-for-approval) ----------

func (h *handlers) addGrantee(ctx context.Context, _ *mcp.CallToolRequest, in *AddGranteeInput) (*mcp.CallToolResult, *EnqueueResult, error) {
	slug := strings.TrimSpace(in.Slug)
	if slug == "" {
		return nil, nil, errors.New("slug is required")
	}
	args, err := json.Marshal(actions.AddGranteeArgs{Slug: slug, DisplayName: in.DisplayName})
	if err != nil {
		return nil, nil, err
	}
	meta, err := actionMetadata(in.SignalNumber)
	if err != nil {
		return nil, nil, err
	}
	id, err := h.st.EnqueueAction(ctx, actions.ActionAddGrantee, args, meta, in.RequestedBy)
	if err != nil {
		return nil, nil, err
	}
	return nil, &EnqueueResult{ApprovalID: id, Action: actions.ActionAddGrantee, Status: store.ApprovalPending}, nil
}

func (h *handlers) addApprovalUser(ctx context.Context, _ *mcp.CallToolRequest, in *AddApprovalUserInput) (*mcp.CallToolResult, *EnqueueResult, error) {
	email := strings.TrimSpace(in.Email)
	if email == "" {
		return nil, nil, errors.New("email is required")
	}
	pubKey := strings.TrimSpace(in.PublicKey)
	if pubKey == "" {
		return nil, nil, errors.New("ssh_public_key is required")
	}
	// Validate the key shape now so the agent gets immediate feedback rather
	// than a failure surfacing only after an operator approves it.
	if _, err := approval.Fingerprint(pubKey); err != nil {
		return nil, nil, fmt.Errorf("invalid ssh_public_key: %w", err)
	}
	args, err := json.Marshal(actions.AddApprovalUserArgs{Email: email, PublicKey: pubKey})
	if err != nil {
		return nil, nil, err
	}
	meta, err := actionMetadata(in.SignalNumber)
	if err != nil {
		return nil, nil, err
	}
	id, err := h.st.EnqueueAction(ctx, actions.ActionAddApprovalUser, args, meta, in.RequestedBy)
	if err != nil {
		return nil, nil, err
	}
	return nil, &EnqueueResult{ApprovalID: id, Action: actions.ActionAddApprovalUser, Status: store.ApprovalPending}, nil
}

func (h *handlers) whitelistDoc(ctx context.Context, _ *mcp.CallToolRequest, in *WhitelistDocInput) (*mcp.CallToolResult, *EnqueueResult, error) {
	grantee := strings.TrimSpace(in.GranteeID)
	if grantee == "" {
		return nil, nil, errors.New("grantee_id is required")
	}
	driveID := strings.TrimSpace(in.DriveID)
	if driveID == "" {
		return nil, nil, errors.New("drive_id is required")
	}
	// Confirm the grantee exists now so the agent gets immediate feedback
	// rather than a failure surfacing only after an operator approves it.
	if _, err := h.st.GetGrantee(ctx, grantee); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, fmt.Errorf("grantee_not_found: %s", grantee)
		}
		return nil, nil, err
	}
	// Classify the id as a folder or a doc from its Drive MIME type, so the
	// executor (and the human approving it) see the resolved type. This is the
	// only Drive call this server makes; it is read-only.
	if h.drv == nil {
		return nil, nil, errors.New("drive lookup is not configured on this server")
	}
	f, err := h.drv.GetFile(ctx, driveID, "")
	if err != nil {
		return nil, nil, fmt.Errorf("look up drive object %q: %w", driveID, err)
	}
	var srcType string
	switch f.MimeType {
	case drive.MimeFolder:
		srcType = store.SourceTypeFolder
	case drive.MimeDoc:
		srcType = store.SourceTypeDoc
	default:
		return nil, nil, fmt.Errorf("drive object %q has mime %q; only Google Docs and folders can be whitelisted", driveID, f.MimeType)
	}
	args, err := json.Marshal(actions.WhitelistDocArgs{GranteeID: grantee, DriveID: driveID, SourceType: srcType})
	if err != nil {
		return nil, nil, err
	}
	meta, err := actionMetadata(in.SignalNumber)
	if err != nil {
		return nil, nil, err
	}
	id, err := h.st.EnqueueAction(ctx, actions.ActionWhitelistDoc, args, meta, in.RequestedBy)
	if err != nil {
		return nil, nil, err
	}
	return nil, &EnqueueResult{ApprovalID: id, Action: actions.ActionWhitelistDoc, Status: store.ApprovalPending}, nil
}

// actionMetadata builds the approval_actions.metadata JSON for an enqueued
// action. It returns nil (letting EnqueueAction default to "{}") when there is
// no request context to record.
func actionMetadata(signalNumber string) (json.RawMessage, error) {
	sn := strings.TrimSpace(signalNumber)
	if sn == "" {
		return nil, nil
	}
	return json.Marshal(actions.Metadata{SignalNumber: sn})
}

// ---------- grantees ----------

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

// ---------- mail ----------

func (h *handlers) listEmailsForGrantee(ctx context.Context, _ *mcp.CallToolRequest, in *ListEmailsForGranteeInput) (*mcp.CallToolResult, *ListEmailsForGranteeOutput, error) {
	if in.GranteeID == "" {
		return nil, nil, errors.New("grantee_id is required")
	}
	since, err := parseTimeBound(in.Since)
	if err != nil {
		return nil, nil, err
	}
	until, err := parseTimeBound(in.Until)
	if err != nil {
		return nil, nil, err
	}
	msgs, next, err := h.st.ListMessagesByGrantee(ctx, in.GranteeID, since, until, in.Limit, in.Cursor)
	if err != nil {
		return nil, nil, err
	}
	if msgs == nil {
		msgs = []store.MessageSummary{}
	}
	return nil, &ListEmailsForGranteeOutput{Messages: msgs, NextCursor: next}, nil
}

func (h *handlers) getEmail(ctx context.Context, _ *mcp.CallToolRequest, in *GetEmailInput) (*mcp.CallToolResult, *GetEmailOutput, error) {
	if in.ID == "" {
		return nil, nil, errors.New("id is required")
	}
	m, atts, recs, err := h.st.GetMessage(ctx, in.ID)
	if err != nil {
		return nil, nil, err
	}
	if m == nil {
		return nil, nil, fmt.Errorf("message %q not found", in.ID)
	}
	detail := &MessageDetail{
		ID:         m.ID,
		MessageID:  m.MessageID,
		ThreadID:   m.ThreadID,
		GranteeID:  m.GranteeID,
		Folder:     m.Folder,
		InReplyTo:  m.InReplyTo,
		References: m.References,
		From:       m.From,
		To:         m.To,
		Cc:         m.Cc,
		Recipients: recs,
		Subject:    m.Subject,
		Date:       m.Date,
		BodyText:   m.BodyText,
		BodyHTML:   m.BodyHTML,
		SizeBytes:  m.SizeBytes,
	}
	return nil, &GetEmailOutput{Message: detail, Attachments: atts}, nil
}

func (h *handlers) listThreadsForGrantee(ctx context.Context, _ *mcp.CallToolRequest, in *ListThreadsForGranteeInput) (*mcp.CallToolResult, *ListThreadsForGranteeOutput, error) {
	if in.GranteeID == "" {
		return nil, nil, errors.New("grantee_id is required")
	}
	since, err := parseTimeBound(in.Since)
	if err != nil {
		return nil, nil, err
	}
	until, err := parseTimeBound(in.Until)
	if err != nil {
		return nil, nil, err
	}
	ts, next, err := h.st.ListThreadsByGrantee(ctx, in.GranteeID, since, until, in.Limit, in.Cursor)
	if err != nil {
		return nil, nil, err
	}
	if ts == nil {
		ts = []store.ThreadSummary{}
	}
	return nil, &ListThreadsForGranteeOutput{Threads: ts, NextCursor: next}, nil
}

func (h *handlers) getThread(ctx context.Context, _ *mcp.CallToolRequest, in *GetThreadInput) (*mcp.CallToolResult, *GetThreadOutput, error) {
	if in.ThreadID == 0 {
		return nil, nil, errors.New("thread_id is required")
	}
	t, msgs, err := h.st.GetThreadMessages(ctx, in.ThreadID)
	if err != nil {
		return nil, nil, err
	}
	if t == nil {
		return nil, nil, fmt.Errorf("thread %d not found", in.ThreadID)
	}
	if msgs == nil {
		msgs = []store.MessageSummary{}
	}
	return nil, &GetThreadOutput{Thread: t, Messages: msgs}, nil
}

func (h *handlers) searchEmails(ctx context.Context, _ *mcp.CallToolRequest, in *SearchEmailsInput) (*mcp.CallToolResult, *SearchEmailsOutput, error) {
	since, err := parseTimeBound(in.Since)
	if err != nil {
		return nil, nil, err
	}
	until, err := parseTimeBound(in.Until)
	if err != nil {
		return nil, nil, err
	}
	msgs, next, err := h.st.SearchMessages(ctx, store.SearchParams{
		GranteeID: in.GranteeID,
		From:      in.From,
		Subject:   in.Subject,
		Body:      in.Body,
		Since:     since,
		Until:     until,
		Limit:     in.Limit,
		Cursor:    in.Cursor,
	})
	if err != nil {
		return nil, nil, err
	}
	if msgs == nil {
		msgs = []store.MessageSummary{}
	}
	return nil, &SearchEmailsOutput{Messages: msgs, NextCursor: next}, nil
}

func (h *handlers) listAttachments(ctx context.Context, _ *mcp.CallToolRequest, in *ListAttachmentsInput) (*mcp.CallToolResult, *ListAttachmentsOutput, error) {
	if in.MessageID == 0 {
		return nil, nil, errors.New("message_id is required")
	}
	atts, err := h.st.ListAttachmentsByMessage(ctx, in.MessageID)
	if err != nil {
		return nil, nil, err
	}
	if atts == nil {
		atts = []store.Attachment{}
	}
	return nil, &ListAttachmentsOutput{Attachments: atts}, nil
}

// ---------- docs ----------

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
