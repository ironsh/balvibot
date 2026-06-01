// Package drive is a tiny REST client over net/http for the Google Drive v3
// endpoints we need: files.list, files.get, files.export.
//
// It authenticates one of two ways, depending on the caller:
//   - TokenSource set: the caller holds a Google service-account credential and
//     talks to Drive directly (the api/MCP server). Tokens are real and
//     refreshed by the token source.
//   - BrokerToken set: every request carries a literal placeholder Bearer token
//     that iron-proxy swaps for a fresh SA token at the egress boundary (the
//     indexer). No OAuth happens in-process, keeping that workload free of
//     Google credentials.
package drive

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
	// BrokerToken is the placeholder Bearer iron-proxy substitutes. We
	// always send this verbatim; we never refresh it. Ignored when
	// TokenSource is set.
	BrokerToken string
	// TokenSource, if set, supplies real OAuth2 access tokens for direct
	// Google API access. When set, the client authenticates with these tokens
	// (refreshed by the source) and BrokerToken/CAFile are not used.
	TokenSource oauth2.TokenSource
	// CAFile, if set, is added to the TLS trust store (typically the
	// iron-proxy CA so we trust the MITM cert on *.googleapis.com).
	CAFile string
	// HTTPClient, if set, is used as-is — bypasses CAFile / Transport
	// construction. Tests use this to point at httptest.Server.
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
	if cfg.TokenSource == nil && cfg.BrokerToken == "" {
		return nil, errors.New("drive: a TokenSource or BrokerToken is required")
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
		if cfg.CAFile != "" {
			pool, err := loadCAPool(cfg.CAFile)
			if err != nil {
				return nil, fmt.Errorf("drive: load CA file %q: %w", cfg.CAFile, err)
			}
			tr.TLSClientConfig = &tls.Config{RootCAs: pool}
		}
		httpClient = &http.Client{Transport: tr, Timeout: cfg.Timeout}
	}

	return &Client{cfg: cfg, http: httpClient}, nil
}

func loadCAPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Start from the system roots so plain TLS still works for anything
	// not MITM'd; iron-proxy's CA is an additional trust anchor.
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certs found in %s", path)
	}
	return pool, nil
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
	if c.cfg.TokenSource != nil {
		// Direct access: a real, source-refreshed SA token.
		tok, err := c.cfg.TokenSource.Token()
		if err != nil {
			return nil, fmt.Errorf("drive: get access token: %w", err)
		}
		tok.SetAuthHeader(req)
	} else {
		// The literal placeholder. iron-proxy swaps this for a real SA token
		// at the wire; we never see, store, or refresh the real one.
		req.Header.Set("Authorization", "Bearer "+c.cfg.BrokerToken)
	}
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
