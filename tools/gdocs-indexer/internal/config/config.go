package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

const (
	DefaultPollInterval = 5 * time.Minute
	DefaultMCPListen    = "127.0.0.1:8800"
	// DefaultBrokerToken is the literal placeholder iron-proxy's gcp_auth
	// transform expects in the Authorization header. iron-proxy swaps it
	// for a real service-account access token at the wire. See
	// https://docs.iron.sh/credential-proxying/gcp-auth.
	DefaultBrokerToken = "iron-proxy-stub-token"
	DefaultCAFile       = "/etc/ssl/iron-proxy/ca.crt"
)

type Config struct {
	DBPath string

	PollInterval time.Duration

	MCPEnabled     bool
	MCPListenAddr  string
	MCPBearerToken string

	// IronProxyURL is informational; egress goes via DNS override + MITM.
	IronProxyURL string
	BrokerToken  string
	CAFile       string

	// DriveBaseURL lets tests / local smoke runs point at a fake Drive.
	// Empty means the real Drive v3 endpoint.
	DriveBaseURL string

	LogLevel slog.Level
}

func FromEnv() (*Config, error) {
	c := &Config{
		DBPath:         os.Getenv("IRON_GDOCS_DB_PATH"),
		MCPListenAddr:  getenvDefault("IRON_GDOCS_MCP_LISTEN_ADDR", DefaultMCPListen),
		MCPBearerToken: os.Getenv("IRON_GDOCS_MCP_BEARER_TOKEN"),
		MCPEnabled:     strings.ToLower(getenvDefault("IRON_GDOCS_MCP_ENABLED", "true")) == "true",
		IronProxyURL:   os.Getenv("IRON_PROXY_URL"),
		BrokerToken:    getenvDefault("IRON_GDOCS_BROKER_TOKEN", DefaultBrokerToken),
		CAFile:         getenvDefault("IRON_PROXY_CA_FILE", DefaultCAFile),
		DriveBaseURL:   os.Getenv("IRON_GDOCS_DRIVE_BASE_URL"),
	}

	if v := os.Getenv("IRON_GDOCS_POLL_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("IRON_GDOCS_POLL_INTERVAL: %w", err)
		}
		c.PollInterval = d
	} else {
		c.PollInterval = DefaultPollInterval
	}

	switch strings.ToLower(getenvDefault("IRON_GDOCS_LOG_LEVEL", "info")) {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "warn":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		c.LogLevel = slog.LevelInfo
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	var missing []string
	if c.DBPath == "" {
		missing = append(missing, "IRON_GDOCS_DB_PATH")
	}
	if c.IronProxyURL == "" {
		missing = append(missing, "IRON_PROXY_URL")
	}
	if c.MCPEnabled && c.MCPBearerToken == "" {
		missing = append(missing, "IRON_GDOCS_MCP_BEARER_TOKEN (set IRON_GDOCS_MCP_ENABLED=false to disable the MCP server)")
	}
	if c.PollInterval < time.Second {
		return fmt.Errorf("IRON_GDOCS_POLL_INTERVAL too small: %s", c.PollInterval)
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	return nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
