package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/ironsh/balvibot/tools/api/internal/db"
	"github.com/ironsh/balvibot/tools/api/internal/drive"
	"github.com/ironsh/balvibot/tools/api/internal/store"
)

// fakeDrive maps a Drive id to the MIME type GetFile should report. Unknown ids
// return a 404, mirroring the real client.
type fakeDrive map[string]string

func (f fakeDrive) GetFile(_ context.Context, fileID, _ string) (*drive.File, error) {
	if mt, ok := f[fileID]; ok {
		return &drive.File{ID: fileID, MimeType: mt}, nil
	}
	return nil, &drive.StatusError{Status: http.StatusNotFound, URL: fileID}
}

// testDrive is the fixed Drive fixture used by the MCP test server.
var testDrive = fakeDrive{
	"folder-x": drive.MimeFolder,
	"doc-x":    drive.MimeDoc,
	"sheet-x":  "application/vnd.google-apps.spreadsheet",
}

// newTestStore connects to DATABASE_URL, migrates, truncates, and seeds two
// grantees (with an email + a doc each) plus one mail message. Skipped when
// DATABASE_URL is unset.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping Postgres MCP test")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, dsn)
	require.NoError(t, err)
	require.NoError(t, db.MigrateUp(pool))
	_, err = pool.ExecContext(ctx, `
		TRUNCATE grantees, grantee_emails, grantee_sources,
		         threads, messages, message_references, message_recipients,
		         attachments, mailbox_state, docs, sync_state,
		         approval_actions, approval_users, notes RESTART IDENTITY CASCADE
	`)
	require.NoError(t, err)
	st := store.New(pool)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.UpsertGrantee(ctx, store.Grantee{GranteeID: "acme", DisplayName: "Acme"}))
	require.NoError(t, st.UpsertGrantee(ctx, store.Grantee{GranteeID: "beta", DisplayName: "Beta"}))
	require.NoError(t, st.AddGranteeEmail(ctx, "acme", "sender@acme.org"))

	cycle, err := st.NextCycleID(ctx)
	require.NoError(t, err)
	require.NoError(t, st.UpsertDoc(ctx, store.Doc{
		DocID: "doc-acme-1", GranteeID: "acme", Title: "Acme One",
		OwnerEmail: "owner@acme.org", ContentMarkdown: "# acme one",
		ModifiedAt: time.Unix(1_700_000_000, 0), SourceType: store.SourceTypeFolder,
		SourceDriveID: "folder-acme", HadImages: true,
	}, cycle))
	require.NoError(t, st.UpsertDoc(ctx, store.Doc{
		DocID: "doc-beta-1", GranteeID: "beta", Title: "Beta One",
		OwnerEmail: "lead@beta.org", ContentMarkdown: "# beta one",
		ModifiedAt: time.Unix(1_700_000_100, 0), SourceType: store.SourceTypeFolder,
		SourceDriveID: "folder-beta",
	}, cycle))

	threadID, err := st.CreateThread(ctx, "root@acme", "kickoff", nil, time.Unix(1_700_000_200, 0))
	require.NoError(t, err)
	gid := "acme"
	_, _, err = st.InsertMessage(ctx, &store.Message{
		MessageID: "m-1@acme", ThreadID: threadID, GranteeID: &gid,
		Folder: "INBOX", UID: 1, UIDValidity: 1,
		From: store.Address{Email: "sender@acme.org"}, Subject: "Kickoff plan",
		Date: time.Unix(1_700_000_200, 0), BodyText: "let's begin", SizeBytes: 10,
	})
	require.NoError(t, err)
	return st
}

func startTestServer(t *testing.T, st *store.Store) (endpoint, token string) {
	t.Helper()
	token = "test-token"
	srv := buildServer(st, testDrive)
	streamHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	mux := http.NewServeMux()
	mux.Handle("/mcp", streamHandler)
	mux.Handle("/mcp/", streamHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	handler := bearerAuth(token, mux)
	httpSrv := httptest.NewServer(handler)
	t.Cleanup(httpSrv.Close)
	return httpSrv.URL + "/mcp", token
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b *bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	base := b.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

func newClientSession(t *testing.T, endpoint, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	httpClient := &http.Client{Transport: &bearerTransport{token: token}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           httpClient,
		DisableStandaloneSSE: true,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func callTool[T any](t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err, "call %s", name)
	if res.IsError {
		var buf strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				buf.WriteString(tc.Text)
			}
		}
		t.Fatalf("tool %s reported error: %s", name, buf.String())
	}
	var out T
	raw, ok := res.StructuredContent.(json.RawMessage)
	if !ok {
		b, _ := json.Marshal(res.StructuredContent)
		raw = b
	}
	require.NoError(t, json.Unmarshal(raw, &out), "unmarshal %s output", name)
	return out
}

func callToolExpectError(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err, "call %s", name)
	require.True(t, res.IsError, "expected tool error for %s", name)
	var buf strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			buf.WriteString(tc.Text)
		}
	}
	return buf.String()
}

func TestHealthzNoAuth(t *testing.T) {
	st := newTestStore(t)
	endpoint, _ := startTestServer(t, st)
	base := strings.TrimSuffix(endpoint, "/mcp")
	resp, err := http.Get(base + "/healthz")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMCPRejectsMissingToken(t *testing.T) {
	st := newTestStore(t)
	endpoint, _ := startTestServer(t, st)
	resp, err := http.Get(endpoint)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestListGranteesMerged exercises the single unified list_grantees tool:
// emails (mail side) + document counts (docs side) come back together.
func TestListGranteesMerged(t *testing.T) {
	st := newTestStore(t)
	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)
	out := callTool[ListGranteesOutput](t, sess, "list_grantees", nil)
	require.Len(t, out.Grantees, 2)
	byID := map[string]store.GranteeSummary{}
	for _, g := range out.Grantees {
		byID[g.GranteeID] = g
	}
	require.Equal(t, 1, byID["acme"].DocumentCount)
	require.Equal(t, []string{"sender@acme.org"}, byID["acme"].Emails)
	require.Equal(t, "Acme", byID["acme"].DisplayName)
	require.Equal(t, 1, byID["beta"].DocumentCount)
}

func TestSearchEmails(t *testing.T) {
	st := newTestStore(t)
	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)
	out := callTool[SearchEmailsOutput](t, sess, "search_emails", map[string]any{"subject": "kickoff"})
	require.Len(t, out.Messages, 1)
	require.Equal(t, "m-1@acme", out.Messages[0].MessageID)
}

func TestListDocumentsForGrantee(t *testing.T) {
	st := newTestStore(t)
	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)
	out := callTool[ListDocumentsForGranteeOutput](t, sess, "list_documents_for_grantee", map[string]any{
		"grantee_id": "acme",
	})
	require.Len(t, out.Documents, 1)
	require.Equal(t, "doc-acme-1", out.Documents[0].DocID)
	require.True(t, out.Documents[0].HadImages)
}

// TestWhitelistDocResolvesType drives the enqueue-time path: the tool looks up
// the Drive id, classifies it, and queues a pending action. A folder and a doc
// id both succeed (with the type resolved); a non-doc/folder MIME and a
// nonexistent grantee are rejected before anything is queued.
func TestWhitelistDocResolvesType(t *testing.T) {
	st := newTestStore(t)
	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)

	folder := callTool[EnqueueResult](t, sess, "whitelist_doc", map[string]any{
		"grantee_id": "acme", "drive_id": "folder-x",
	})
	require.Equal(t, "whitelist_doc", folder.Action)
	require.Equal(t, store.ApprovalPending, folder.Status)
	require.NotZero(t, folder.ApprovalID)

	doc := callTool[EnqueueResult](t, sess, "whitelist_doc", map[string]any{
		"grantee_id": "acme", "drive_id": "doc-x",
	})
	require.NotEqual(t, folder.ApprovalID, doc.ApprovalID)

	// A spreadsheet (neither folder nor Google Doc) is refused.
	msg := callToolExpectError(t, sess, "whitelist_doc", map[string]any{
		"grantee_id": "acme", "drive_id": "sheet-x",
	})
	require.Contains(t, msg, "only Google Docs and folders")

	// An unknown grantee is refused before the Drive lookup.
	msg = callToolExpectError(t, sess, "whitelist_doc", map[string]any{
		"grantee_id": "ghost", "drive_id": "doc-x",
	})
	require.Contains(t, msg, "grantee_not_found")
}

// TestListApprovals enqueues a couple of actions and reads them back, both
// unfiltered (newest first) and filtered by status. An invalid status is
// rejected.
func TestListApprovals(t *testing.T) {
	st := newTestStore(t)
	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)

	// Empty queue returns an empty (non-null) list.
	empty := callTool[ListApprovalsOutput](t, sess, "list_approvals", map[string]any{})
	require.Empty(t, empty.Approvals)

	first := callTool[EnqueueResult](t, sess, "add_grantee", map[string]any{
		"slug": "gamma", "requested_by": "tester",
	})
	second := callTool[EnqueueResult](t, sess, "whitelist_doc", map[string]any{
		"grantee_id": "acme", "drive_id": "doc-x",
	})

	all := callTool[ListApprovalsOutput](t, sess, "list_approvals", map[string]any{})
	require.Len(t, all.Approvals, 2)
	// Newest first: the whitelist_doc action precedes the add_grantee one.
	require.Equal(t, second.ApprovalID, all.Approvals[0].ID)
	require.Equal(t, "whitelist_doc", all.Approvals[0].Action)
	// Args round-trips as a decoded object (not a raw byte array), which is
	// what the output schema now advertises.
	require.Equal(t, "acme", all.Approvals[0].Args["grantee_id"])
	require.Equal(t, "doc-x", all.Approvals[0].Args["drive_id"])
	require.Equal(t, first.ApprovalID, all.Approvals[1].ID)
	require.Equal(t, store.ApprovalPending, all.Approvals[1].Status)
	require.Equal(t, "tester", all.Approvals[1].RequestedBy)

	// Status filter narrows the set.
	pending := callTool[ListApprovalsOutput](t, sess, "list_approvals", map[string]any{
		"status": store.ApprovalPending,
	})
	require.Len(t, pending.Approvals, 2)
	executed := callTool[ListApprovalsOutput](t, sess, "list_approvals", map[string]any{
		"status": store.ApprovalExecuted,
	})
	require.Empty(t, executed.Approvals)

	msg := callToolExpectError(t, sess, "list_approvals", map[string]any{"status": "bogus"})
	require.Contains(t, msg, "invalid status")
}

// TestAuthorizeGranteeEmail drives the enqueue-time path: a valid request queues
// a pending action carrying the grantee/email args, while an unknown grantee or a
// blank email is refused before anything is queued.
func TestAuthorizeGranteeEmail(t *testing.T) {
	st := newTestStore(t)
	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)

	res := callTool[EnqueueResult](t, sess, "authorize_grantee_email", map[string]any{
		"grantee_id": "acme", "email": "new-sender@acme.org",
	})
	require.Equal(t, "authorize_grantee_email", res.Action)
	require.Equal(t, store.ApprovalPending, res.Status)
	require.NotZero(t, res.ApprovalID)

	// The queued action carries the grantee and email args verbatim; nothing is
	// applied yet (the mapping happens only at approval time).
	all := callTool[ListApprovalsOutput](t, sess, "list_approvals", map[string]any{})
	require.Len(t, all.Approvals, 1)
	require.Equal(t, "acme", all.Approvals[0].Args["grantee_id"])
	require.Equal(t, "new-sender@acme.org", all.Approvals[0].Args["email"])

	// An unknown grantee is refused.
	msg := callToolExpectError(t, sess, "authorize_grantee_email", map[string]any{
		"grantee_id": "ghost", "email": "x@ghost.org",
	})
	require.Contains(t, msg, "grantee_not_found")

	// A blank email is refused.
	msg = callToolExpectError(t, sess, "authorize_grantee_email", map[string]any{
		"grantee_id": "acme", "email": "  ",
	})
	require.Contains(t, msg, "email is required")
}

// TestNotes drives the create_note/list_notes round trip: a note writes
// immediately (no approval), supersedes_id hides the old note by default and
// surfaces it with include_superseded, and bad inputs are refused.
func TestNotes(t *testing.T) {
	st := newTestStore(t)
	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)

	created := callTool[CreateNoteOutput](t, sess, "create_note", map[string]any{
		"grantee_id": "acme", "content": "prefers async updates",
		"kind": "preference", "signal_number": "+15551234567",
	})
	require.NotNil(t, created.Note)
	require.NotZero(t, created.Note.ID)
	require.Equal(t, "preference", created.Note.Kind)
	require.Equal(t, "+15551234567", created.Note.SignalNumber)

	listed := callTool[ListNotesOutput](t, sess, "list_notes", map[string]any{"grantee_id": "acme"})
	require.Len(t, listed.Notes, 1)
	require.Equal(t, created.Note.ID, listed.Notes[0].ID)

	// Supersede it; the old note drops out of the default list.
	replaced := callTool[CreateNoteOutput](t, sess, "create_note", map[string]any{
		"grantee_id": "acme", "content": "prefers a weekly call after all",
		"kind": "preference", "supersedes_id": created.Note.ID,
	})
	require.Equal(t, created.Note.ID, *replaced.Note.SupersedesID)

	current := callTool[ListNotesOutput](t, sess, "list_notes", map[string]any{"grantee_id": "acme"})
	require.Len(t, current.Notes, 1)
	require.Equal(t, replaced.Note.ID, current.Notes[0].ID)

	all := callTool[ListNotesOutput](t, sess, "list_notes", map[string]any{
		"grantee_id": "acme", "include_superseded": true,
	})
	require.Len(t, all.Notes, 2)

	// Unknown grantee, blank content, bad kind, and cross-grantee supersede are
	// all refused.
	msg := callToolExpectError(t, sess, "create_note", map[string]any{"grantee_id": "ghost", "content": "x"})
	require.Contains(t, msg, "grantee_not_found")
	msg = callToolExpectError(t, sess, "create_note", map[string]any{"grantee_id": "acme", "content": "  "})
	require.Contains(t, msg, "content is required")
	msg = callToolExpectError(t, sess, "create_note", map[string]any{"grantee_id": "acme", "content": "x", "kind": "bogus"})
	require.Contains(t, msg, "invalid kind")
	msg = callToolExpectError(t, sess, "create_note", map[string]any{
		"grantee_id": "beta", "content": "x", "supersedes_id": created.Note.ID,
	})
	require.Contains(t, msg, "belongs to grantee acme")
}

func TestGetDocumentCrossGranteeRefused(t *testing.T) {
	st := newTestStore(t)
	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)
	msg := callToolExpectError(t, sess, "get_document_for_grantee", map[string]any{
		"grantee_id": "beta", "doc_id": "doc-acme-1",
	})
	require.Contains(t, msg, "document_not_found")
}
