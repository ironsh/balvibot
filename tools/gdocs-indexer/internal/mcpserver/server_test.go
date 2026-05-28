package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/ironcd/philanthropy-os/tools/gdocs-indexer/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	require.NoError(t, st.UpsertGrantee(ctx, store.Grantee{GranteeID: "acme", OwnerEmail: "owner@acme.org", DisplayName: "Acme"}))
	require.NoError(t, st.UpsertGrantee(ctx, store.Grantee{GranteeID: "beta", OwnerEmail: "lead@beta.org", DisplayName: "Beta"}))

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
	require.NoError(t, json.Unmarshal(raw, &out), "unmarshal %s output: %s", name, string(raw))
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

func TestListGrantees(t *testing.T) {
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
	require.Equal(t, 1, byID["beta"].DocumentCount)
	require.Equal(t, "Acme", byID["acme"].DisplayName)
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

func TestListDocumentsForGranteeUnknownGrantee(t *testing.T) {
	st := newTestStore(t)
	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)
	msg := callToolExpectError(t, sess, "list_documents_for_grantee", map[string]any{
		"grantee_id": "does-not-exist",
	})
	require.Contains(t, msg, "grantee_not_found")
}

func TestGetDocumentForGrantee(t *testing.T) {
	st := newTestStore(t)
	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)
	out := callTool[GetDocumentOutput](t, sess, "get_document_for_grantee", map[string]any{
		"grantee_id": "acme", "doc_id": "doc-acme-1",
	})
	require.NotNil(t, out.Document)
	require.Equal(t, "# acme one", out.Document.ContentMarkdown)
	require.Equal(t, "doc-acme-1", out.Document.DocID)
	require.False(t, out.Document.Stale)
}

func TestGetDocumentForGranteeCrossGranteeRefused(t *testing.T) {
	// Acceptance criterion #8: getting a doc_id with the wrong grantee_id
	// must return document_not_found, not the content.
	st := newTestStore(t)
	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)
	msg := callToolExpectError(t, sess, "get_document_for_grantee", map[string]any{
		"grantee_id": "beta", "doc_id": "doc-acme-1",
	})
	require.Contains(t, msg, "document_not_found")
}

func TestGetDocumentMetadataOmitsBody(t *testing.T) {
	st := newTestStore(t)
	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)
	res := callTool[GetDocumentMetadataOutput](t, sess, "get_document_metadata", map[string]any{
		"grantee_id": "acme", "doc_id": "doc-acme-1",
	})
	require.NotNil(t, res.Document)
	require.Equal(t, "Acme One", res.Document.Title)
	// The metadata struct doesn't expose content; the marshalled JSON
	// confirms it isn't present.
	raw, err := json.Marshal(res.Document)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "content_markdown")
}

func TestStaleDocSurfacesFlag(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, st.MarkDocStale(ctx, "doc-acme-1", "lost access"))
	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)
	out := callTool[GetDocumentOutput](t, sess, "get_document_for_grantee", map[string]any{
		"grantee_id": "acme", "doc_id": "doc-acme-1",
	})
	require.True(t, out.Document.Stale, "stale docs must be flagged")
	require.Equal(t, "lost access", out.Document.LastError)
	require.NotEmpty(t, out.Document.ContentMarkdown, "stale must return cached content")
}

func TestUnregisteredDocsNotReachable(t *testing.T) {
	// The MCP server must NOT expose any way to read unregistered docs.
	st := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, st.UpsertUnregisteredDoc(ctx, store.UnregisteredDoc{
		DocID: "spy-1", Title: "Spy", OwnerEmail: "spy@example.org",
	}))
	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)
	ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tools, err := sess.ListTools(ctx2, &mcp.ListToolsParams{})
	require.NoError(t, err)
	for _, tool := range tools.Tools {
		require.NotContains(t, strings.ToLower(tool.Name), "unregister")
		require.NotContains(t, strings.ToLower(tool.Name), "shadow")
		require.NotContains(t, strings.ToLower(tool.Name), "block")
	}
	// And the doc isn't reachable as a regular doc.
	msg := callToolExpectError(t, sess, "get_document_for_grantee", map[string]any{
		"grantee_id": "acme", "doc_id": "spy-1",
	})
	require.Contains(t, msg, "document_not_found")
}
