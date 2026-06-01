// Package drive is a tiny REST client over net/http for the Google Drive v3
// endpoints we need: files.list, files.get, files.export.
//
// Callers authenticate with a Google service-account credential via
// Config.TokenSource; the client sends the real, source-refreshed access token
// on every request and talks to Drive directly.
package drive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// DefaultBaseURL is the public Drive v3 endpoint. Override with
// Config.BaseURL for tests.
const DefaultBaseURL = "https://www.googleapis.com/drive/v3"

// StatusError captures non-2xx responses so callers can detect 403/404 via
// errors.As.
type StatusError struct {
	Status int
	URL    string
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("drive: HTTP %d from %s: %s", e.Status, e.URL, truncate(e.Body, 256))
}

// IsNotFoundOrForbidden returns true if err wraps a StatusError with status
// 403 or 404 — both of which we treat as "the SA lost access to this doc".
func IsNotFoundOrForbidden(err error) bool {
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	return se.Status == http.StatusNotFound || se.Status == http.StatusForbidden
}

type Config struct {
	// BaseURL is the Drive v3 endpoint root. Defaults to DefaultBaseURL.
	BaseURL string
	// TokenSource supplies OAuth2 access tokens for Google API access. The
	// client sends these (refreshed by the source) on every request. Required.
	TokenSource oauth2.TokenSource
	// HTTPClient, if set, is used as-is — bypasses Transport construction.
	// Tests use this to point at an httptest.Server.
	HTTPClient *http.Client
	// Timeout for individual HTTP requests. Defaults to 30s.
	Timeout time.Duration
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.TokenSource == nil {
		return nil, errors.New("drive: a TokenSource is required")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		tr := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
		httpClient = &http.Client{Transport: tr, Timeout: cfg.Timeout}
	}

	return &Client{cfg: cfg, http: httpClient}, nil
}

// ListFolder pages through files.list with the q clause restricted to
// children of folderID. pageToken == "" starts a fresh listing.
func (c *Client) ListFolder(ctx context.Context, folderID, pageToken string) (*FileList, error) {
	q := fmt.Sprintf("'%s' in parents and trashed = false", escape(folderID))
	return c.list(ctx, q, pageToken)
}

func (c *Client) list(ctx context.Context, q, pageToken string) (*FileList, error) {
	values := url.Values{}
	values.Set("q", q)
	values.Set("fields", "files(id,name,mimeType,modifiedTime,sharedWithMeTime,owners(emailAddress,displayName),trashed),nextPageToken")
	values.Set("pageSize", "1000")
	if pageToken != "" {
		values.Set("pageToken", pageToken)
	}
	endpoint := c.cfg.BaseURL + "/files?" + values.Encode()
	var out FileList
	if err := c.doJSON(ctx, http.MethodGet, endpoint, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetFile fetches a single file's metadata. fields defaults to a sensible
// subset when blank.
func (c *Client) GetFile(ctx context.Context, fileID, fields string) (*File, error) {
	if fields == "" {
		fields = "id,name,mimeType,modifiedTime,owners(emailAddress,displayName),trashed"
	}
	values := url.Values{}
	values.Set("fields", fields)
	endpoint := c.cfg.BaseURL + "/files/" + url.PathEscape(fileID) + "?" + values.Encode()
	var out File
	if err := c.doJSON(ctx, http.MethodGet, endpoint, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ExportMarkdown exports a Google Doc as text/markdown. Returns the raw
// markdown content as a string.
func (c *Client) ExportMarkdown(ctx context.Context, fileID string) (string, error) {
	values := url.Values{}
	values.Set("mimeType", "text/markdown")
	endpoint := c.cfg.BaseURL + "/files/" + url.PathEscape(fileID) + "/export?" + values.Encode()
	body, err := c.doRaw(ctx, http.MethodGet, endpoint)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, out any) error {
	body, err := c.doRaw(ctx, method, endpoint)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("drive: decode %s: %w", endpoint, err)
	}
	return nil
}

func (c *Client) doRaw(ctx context.Context, method, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return nil, err
	}
	tok, err := c.cfg.TokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("drive: get access token: %w", err)
	}
	tok.SetAuthHeader(req)
	req.Header.Set("Accept", "application/json, */*")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("drive: %s %s: %w", method, endpoint, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("drive: read %s: %w", endpoint, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &StatusError{Status: resp.StatusCode, URL: endpoint, Body: string(body)}
	}
	return body, nil
}

// escape replaces single quotes in a Drive query literal. Drive's q syntax
// requires escaping ' as \'.
func escape(s string) string {
	return strings.ReplaceAll(s, "'", `\'`)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
