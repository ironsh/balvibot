package drive

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeDrive struct {
	t           *testing.T
	mu          sync.Mutex
	authHeaders []string
	// Optional canned responses keyed by path.
	listResponses map[string]string
	files         map[string]string // fileID -> get JSON
	exports       map[string]string // fileID -> markdown body
	exportStatus  map[string]int    // fileID -> status code override for export
}

func newFakeDrive(t *testing.T) *fakeDrive {
	return &fakeDrive{
		t:             t,
		listResponses: map[string]string{},
		files:         map[string]string{},
		exports:       map[string]string{},
		exportStatus:  map[string]int{},
	}
}

func (f *fakeDrive) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.authHeaders = append(f.authHeaders, r.Header.Get("Authorization"))
		f.mu.Unlock()

		switch {
		case r.URL.Path == "/files":
			// Match on the q parameter to choose a response.
			q := r.URL.Query().Get("q")
			body, ok := f.listResponses[q]
			if !ok {
				http.Error(w, "no canned response for q="+q, http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		case strings.HasSuffix(r.URL.Path, "/export") && strings.HasPrefix(r.URL.Path, "/files/"):
			fileID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/files/"), "/export")
			if code, ok := f.exportStatus[fileID]; ok {
				http.Error(w, "boom", code)
				return
			}
			body, ok := f.exports[fileID]
			if !ok {
				http.Error(w, "no canned export for "+fileID, http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(body))
		case strings.HasPrefix(r.URL.Path, "/files/"):
			fileID := strings.TrimPrefix(r.URL.Path, "/files/")
			body, ok := f.files[fileID]
			if !ok {
				http.Error(w, "no canned file for "+fileID, http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		default:
			http.Error(w, "unhandled "+r.URL.Path, http.StatusNotFound)
		}
	})
}

func newTestClient(t *testing.T, f *fakeDrive) *Client {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	c, err := New(Config{
		BaseURL:     srv.URL,
		BrokerToken: "IRON_BROKER:gdocs-sa:default",
		HTTPClient:  srv.Client(),
	})
	require.NoError(t, err)
	return c
}

func TestPlaceholderHeaderForwardedVerbatim(t *testing.T) {
	f := newFakeDrive(t)
	q := fmt.Sprintf("'%s' in parents and trashed = false", "folder-1")
	f.listResponses[q] = `{"files":[]}`
	c := newTestClient(t, f)

	_, err := c.ListFolder(context.Background(), "folder-1", "")
	require.NoError(t, err)
	require.Len(t, f.authHeaders, 1)
	require.Equal(t, "Bearer IRON_BROKER:gdocs-sa:default", f.authHeaders[0],
		"the indexer must forward the placeholder verbatim; iron-proxy does the swap")
}

func TestListFolderPagination(t *testing.T) {
	f := newFakeDrive(t)
	q := fmt.Sprintf("'%s' in parents and trashed = false", "folder-1")
	f.listResponses[q] = `{"files":[{"id":"doc-1","name":"A","mimeType":"application/vnd.google-apps.document"}],"nextPageToken":"tok"}`
	c := newTestClient(t, f)
	page, err := c.ListFolder(context.Background(), "folder-1", "")
	require.NoError(t, err)
	require.Equal(t, "tok", page.NextPageToken)
	require.Len(t, page.Files, 1)
	require.Equal(t, "doc-1", page.Files[0].ID)
}

func TestGetFile(t *testing.T) {
	f := newFakeDrive(t)
	f.files["doc-1"] = `{"id":"doc-1","name":"Hello","mimeType":"application/vnd.google-apps.document","modifiedTime":"2026-01-02T03:04:05Z","owners":[{"emailAddress":"o@a.org"}]}`
	c := newTestClient(t, f)
	file, err := c.GetFile(context.Background(), "doc-1", "")
	require.NoError(t, err)
	require.Equal(t, "Hello", file.Name)
	require.Equal(t, "o@a.org", file.PrimaryOwnerEmail())
}

func TestExportMarkdown(t *testing.T) {
	f := newFakeDrive(t)
	f.exports["doc-1"] = "# Hello\n\nworld"
	c := newTestClient(t, f)
	md, err := c.ExportMarkdown(context.Background(), "doc-1")
	require.NoError(t, err)
	require.Equal(t, "# Hello\n\nworld", md)
}

func TestExport404IsTypedStatusError(t *testing.T) {
	f := newFakeDrive(t)
	f.exportStatus["doc-1"] = http.StatusNotFound
	c := newTestClient(t, f)
	_, err := c.ExportMarkdown(context.Background(), "doc-1")
	require.Error(t, err)
	require.True(t, IsNotFoundOrForbidden(err))
	var se *StatusError
	require.True(t, errors.As(err, &se))
	require.Equal(t, http.StatusNotFound, se.Status)
}

func TestExport403IsTypedStatusError(t *testing.T) {
	f := newFakeDrive(t)
	f.exportStatus["doc-1"] = http.StatusForbidden
	c := newTestClient(t, f)
	_, err := c.ExportMarkdown(context.Background(), "doc-1")
	require.True(t, IsNotFoundOrForbidden(err))
}

func TestRequireBrokerToken(t *testing.T) {
	_, err := New(Config{BaseURL: "http://x"})
	require.Error(t, err)
}

func TestListEncodesPageToken(t *testing.T) {
	var capturedToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedToken = r.URL.Query().Get("pageToken")
		_, _ = w.Write([]byte(`{"files":[]}`))
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{BaseURL: srv.URL, BrokerToken: "x", HTTPClient: srv.Client()})
	require.NoError(t, err)
	_, err = c.ListFolder(context.Background(), "folder-1", "abc 123/&=")
	require.NoError(t, err)
	require.NotEmpty(t, capturedToken)
	require.NotEqual(t, capturedToken, "")
	// Sanity check: round-tripping through net/url preserves the value.
	decoded, err := url.QueryUnescape(url.QueryEscape("abc 123/&="))
	require.NoError(t, err)
	require.Equal(t, decoded, capturedToken)
}
