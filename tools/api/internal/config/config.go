// Package config loads the unified API service configuration from the
// environment. A single Config holds the shared Postgres DSN plus the
// mail-indexer, gdocs-sync, and MCP sections; each subcommand validates only
// the fields it needs via the Require* helpers.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

const (
	DefaultPollInterval     = 5 * time.Minute
	DefaultMCPBindAddr      = ":8080"
	DefaultApprovalBindAddr = ":8090"
	// DefaultBrokerToken is the literal placeholder iron-proxy's gcp_auth
	// transform expects in the Authorization header. iron-proxy swaps it for
	// a real service-account access token at the wire.
	DefaultBrokerToken = "iron-proxy-stub-token"
	DefaultCAFile      = "/etc/ssl/iron-proxy/ca.crt"
)

type Config struct {
	// Shared.
	DatabaseURL string
	LogLevel    slog.Level

	// MCP server (serve).
	MCPBindAddr    string
	MCPBearerToken string
	// GoogleSAKeyFile is the path to a Google service-account JSON key. When
	// set, the MCP server talks to Drive directly (whitelist_doc's folder-vs-doc
	// classification); empty disables whitelist_doc.
	GoogleSAKeyFile string

	// Approval service (approve-serve).
	ApprovalBindAddr string
	// Bootstrap operator, seeded by `api migrate up`. The fingerprint is
	// derived from the public key, so only the email + authorized_keys line are
	// supplied. Both empty = no bootstrap.
	ApprovalBootstrapEmail  string
	ApprovalBootstrapPubKey string

	// Mail indexer (index-mail).
	IMAPAddr       string
	IMAPUser       string
	IMAPPass       string
	IMAPTLS        string
	AttachmentsDir string
	Folders        []string

	// Gdocs sync (sync-gdocs).
	PollInterval time.Duration
	IronProxyURL string
	BrokerToken  string
	CAFile       string
	// DriveBaseURL lets tests / local smoke runs point at a fake Drive.
	DriveBaseURL string
}

// FromEnv reads every known env var. It does NOT validate subcommand-specific
// requirements; call the Require* method matching the subcommand.
func FromEnv() (*Config, error) {
	c := &Config{
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		MCPBindAddr:     getenvDefault("MCP_BIND_ADDR", DefaultMCPBindAddr),
		MCPBearerToken:  os.Getenv("MCP_BEARER_TOKEN"),
		GoogleSAKeyFile: strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")),

		ApprovalBindAddr: getenvDefault("APPROVAL_BIND_ADDR", DefaultApprovalBindAddr),

		ApprovalBootstrapEmail:  strings.TrimSpace(os.Getenv("APPROVAL_BOOTSTRAP_EMAIL")),
		ApprovalBootstrapPubKey: strings.TrimSpace(os.Getenv("APPROVAL_BOOTSTRAP_PUBKEY")),

		IMAPAddr:       os.Getenv("IMAP_ADDR"),
		IMAPUser:       os.Getenv("IMAP_USER"),
		IMAPPass:       os.Getenv("IMAP_PASS"),
		IMAPTLS:        getenvDefault("IMAP_TLS", "starttls"),
		AttachmentsDir: os.Getenv("MAIL_ATTACHMENTS_DIR"),

		IronProxyURL: os.Getenv("IRON_PROXY_URL"),
		BrokerToken:  getenvDefault("GDOCS_BROKER_TOKEN", DefaultBrokerToken),
		CAFile:       getenvDefault("IRON_PROXY_CA_FILE", DefaultCAFile),
		DriveBaseURL: os.Getenv("GDOCS_DRIVE_BASE_URL"),
	}

	folders := getenvDefault("MAIL_FOLDERS", "INBOX,Sent")
	for _, f := range strings.Split(folders, ",") {
		if f = strings.TrimSpace(f); f != "" {
			c.Folders = append(c.Folders, f)
		}
	}

	if v := os.Getenv("GDOCS_POLL_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("GDOCS_POLL_INTERVAL: %w", err)
		}
		c.PollInterval = d
	} else {
		c.PollInterval = DefaultPollInterval
	}

	switch strings.ToLower(getenvDefault("LOG_LEVEL", "info")) {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "warn":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		c.LogLevel = slog.LevelInfo
	}

	return c, nil
}

// RequireDB validates the shared Postgres DSN (every subcommand needs it).
func (c *Config) RequireDB() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("missing required env var: DATABASE_URL")
	}
	return nil
}

// RequireMCP validates the serve subcommand's requirements.
func (c *Config) RequireMCP() error {
	if err := c.RequireDB(); err != nil {
		return err
	}
	if c.MCPBindAddr == "" {
		return fmt.Errorf("MCP_BIND_ADDR is required")
	}
	if c.MCPBearerToken == "" {
		return fmt.Errorf("MCP_BEARER_TOKEN is required")
	}
	return nil
}

// RequireApproval validates the approve-serve subcommand's requirements.
func (c *Config) RequireApproval() error {
	if err := c.RequireDB(); err != nil {
		return err
	}
	if c.ApprovalBindAddr == "" {
		return fmt.Errorf("APPROVAL_BIND_ADDR is required")
	}
	return nil
}

// RequireMail validates the index-mail subcommand's requirements.
func (c *Config) RequireMail() error {
	if err := c.RequireDB(); err != nil {
		return err
	}
	var missing []string
	if c.IMAPAddr == "" {
		missing = append(missing, "IMAP_ADDR")
	}
	if c.IMAPUser == "" {
		missing = append(missing, "IMAP_USER")
	}
	if c.IMAPPass == "" {
		missing = append(missing, "IMAP_PASS")
	}
	if c.AttachmentsDir == "" {
		missing = append(missing, "MAIL_ATTACHMENTS_DIR")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	switch c.IMAPTLS {
	case "starttls", "tls", "none":
	default:
		return fmt.Errorf("IMAP_TLS must be one of starttls|tls|none, got %q", c.IMAPTLS)
	}
	if len(c.Folders) == 0 {
		return fmt.Errorf("MAIL_FOLDERS produced an empty folder list")
	}
	return nil
}

// RequireGdocs validates the sync-gdocs subcommand's requirements.
func (c *Config) RequireGdocs() error {
	if err := c.RequireDB(); err != nil {
		return err
	}
	if c.IronProxyURL == "" {
		return fmt.Errorf("missing required env var: IRON_PROXY_URL")
	}
	if c.PollInterval < time.Second {
		return fmt.Errorf("GDOCS_POLL_INTERVAL too small: %s", c.PollInterval)
	}
	return nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
