package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ironcd/philanthropy-os/tools/mail-indexer/internal/config"
	"github.com/ironcd/philanthropy-os/tools/mail-indexer/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.ReconcileGrantees(context.Background(), []config.Grantee{
		{ID: "acme", Name: "Acme Foundation", Emails: []string{"contact@acme.org", "ops@acme.org"}},
		{ID: "beta", Name: "Beta Org", Emails: []string{"hello@beta.org"}},
	}); err != nil {
		t.Fatalf("seed grantees: %v", err)
	}
	return st
}

var uidCounter uint32

func seedMessage(t *testing.T, st *store.Store, messageID, from, subject, body string, granteeID *string, when time.Time) int64 {
	t.Helper()
	threadID, err := st.CreateThread(context.Background(), messageID, subject, granteeID, when)
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	uidCounter++
	id, _, err := st.InsertMessage(context.Background(), &store.Message{
		MessageID:   messageID,
		ThreadID:    threadID,
		GranteeID:   granteeID,
		Folder:      "INBOX",
		UID:         uidCounter,
		UIDValidity: 1,
		From:        store.Address{Email: from},
		To:          []store.Address{{Email: "us@example.org"}},
		Subject:     subject,
		Date:        when,
		BodyText:    body,
		SizeBytes:   int64(len(body)),
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	return id
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
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func callTool[T any](t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
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
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %s output: %v (raw=%s)", name, err, string(raw))
	}
	return out
}

func TestBearerAuth_Healthz(t *testing.T) {
	st := newTestStore(t)
	endpoint, _ := startTestServer(t, st)
	base := strings.TrimSuffix(endpoint, "/mcp")
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("get healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("healthz: want 200, got %d", resp.StatusCode)
	}
}

func TestBearerAuth_MCPRejectsMissingToken(t *testing.T) {
	st := newTestStore(t)
	endpoint, _ := startTestServer(t, st)
	resp, err := http.Post(endpoint, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestBearerAuth_MCPRejectsWrongToken(t *testing.T) {
	st := newTestStore(t)
	endpoint, _ := startTestServer(t, st)
	req, _ := http.NewRequest("POST", endpoint, strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer wrong")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestListGrantees(t *testing.T) {
	st := newTestStore(t)
	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)

	out := callTool[ListGranteesOutput](t, sess, "list_grantees", nil)
	if len(out.Grantees) != 2 {
		t.Fatalf("want 2 grantees, got %d", len(out.Grantees))
	}
	byID := map[string]store.Grantee{}
	for _, g := range out.Grantees {
		byID[g.ID] = g
	}
	if g := byID["acme"]; len(g.Emails) != 2 {
		t.Errorf("acme: want 2 emails, got %v", g.Emails)
	}
	if g := byID["beta"]; g.Name != "Beta Org" {
		t.Errorf("beta name: got %q", g.Name)
	}
}

func TestListEmailsForGranteeAndPagination(t *testing.T) {
	st := newTestStore(t)
	acme := "acme"
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		seedMessage(t, st, fmt.Sprintf("<m%d@acme.org>", i),
			"contact@acme.org", fmt.Sprintf("Subject %d", i),
			"hello world", &acme, base.Add(time.Duration(i)*time.Hour))
	}
	// One message for beta that must not appear under acme.
	beta := "beta"
	seedMessage(t, st, "<n0@beta.org>", "hello@beta.org", "Beta", "x", &beta, base)
	// Unassigned.
	seedMessage(t, st, "<u0@unknown>", "stranger@example.com", "Unknown", "y", nil, base)

	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)

	page1 := callTool[ListEmailsForGranteeOutput](t, sess, "list_emails_for_grantee", map[string]any{
		"grantee_id": "acme",
		"limit":      3,
	})
	if len(page1.Messages) != 3 {
		t.Fatalf("page1: want 3 messages, got %d", len(page1.Messages))
	}
	if page1.NextCursor == 0 {
		t.Fatal("expected next_cursor on page1")
	}
	for _, m := range page1.Messages {
		if m.GranteeID == nil || *m.GranteeID != "acme" {
			t.Errorf("expected grantee acme, got %v", m.GranteeID)
		}
	}

	page2 := callTool[ListEmailsForGranteeOutput](t, sess, "list_emails_for_grantee", map[string]any{
		"grantee_id": "acme",
		"limit":      3,
		"cursor":     page1.NextCursor,
	})
	if len(page2.Messages) != 2 {
		t.Fatalf("page2: want 2 messages, got %d", len(page2.Messages))
	}
	if page2.NextCursor != 0 {
		t.Errorf("expected no next_cursor on page2, got %d", page2.NextCursor)
	}

	unassigned := callTool[ListEmailsForGranteeOutput](t, sess, "list_emails_for_grantee", map[string]any{
		"grantee_id": store.UnassignedGrantee,
	})
	if len(unassigned.Messages) != 1 {
		t.Fatalf("unassigned: want 1, got %d", len(unassigned.Messages))
	}
	if unassigned.Messages[0].GranteeID != nil {
		t.Errorf("expected nil grantee, got %v", unassigned.Messages[0].GranteeID)
	}
}

func TestGetEmail_ByIDAndMessageID(t *testing.T) {
	st := newTestStore(t)
	acme := "acme"
	id := seedMessage(t, st, "<x1@acme.org>", "contact@acme.org", "Hi", "body text",
		&acme, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))

	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)

	byNumeric := callTool[GetEmailOutput](t, sess, "get_email", map[string]any{"id": fmt.Sprintf("%d", id)})
	if byNumeric.Message == nil || byNumeric.Message.Subject != "Hi" {
		t.Fatalf("by numeric id: got %+v", byNumeric.Message)
	}
	byMsgID := callTool[GetEmailOutput](t, sess, "get_email", map[string]any{"id": "<x1@acme.org>"})
	if byMsgID.Message == nil || byMsgID.Message.ID != id {
		t.Fatalf("by message_id: got %+v", byMsgID.Message)
	}
	if byMsgID.Message.BodyText != "body text" {
		t.Errorf("body_text: got %q", byMsgID.Message.BodyText)
	}
}

func TestSearchEmails(t *testing.T) {
	st := newTestStore(t)
	acme := "acme"
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	seedMessage(t, st, "<s1@acme.org>", "ceo@acme.org", "Quarterly report", "numbers", &acme, base)
	seedMessage(t, st, "<s2@acme.org>", "cfo@acme.org", "Budget", "more numbers", &acme, base.Add(time.Hour))

	endpoint, token := startTestServer(t, st)
	sess := newClientSession(t, endpoint, token)

	out := callTool[SearchEmailsOutput](t, sess, "search_emails", map[string]any{
		"grantee_id": "acme",
		"subject":    "budget",
	})
	if len(out.Messages) != 1 || out.Messages[0].Subject != "Budget" {
		t.Fatalf("subject search: got %+v", out.Messages)
	}

	out = callTool[SearchEmailsOutput](t, sess, "search_emails", map[string]any{
		"from": "cfo",
	})
	if len(out.Messages) != 1 || !strings.Contains(out.Messages[0].From.Email, "cfo") {
		t.Fatalf("from search: got %+v", out.Messages)
	}
}
