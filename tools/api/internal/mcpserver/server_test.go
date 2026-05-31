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
	"github.com/ironsh/balvibot/tools/api/internal/store"
)

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
		         attachments, mailbox_state, docs, unregistered_docs,
		         blocked_owners, sync_state RESTART IDENTITY CASCADE
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
	srv := buildServer(st)
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

func TestGetDocumentCrossGranteeRefused(t *testing.T) {
	st := newTestStore(t)
	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)
	msg := callToolExpectError(t, sess, "get_document_for_grantee", map[string]any{
		"grantee_id": "beta", "doc_id": "doc-acme-1",
	})
	require.Contains(t, msg, "document_not_found")
}
