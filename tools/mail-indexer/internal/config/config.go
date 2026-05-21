package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

type Config struct {
	IMAPAddr       string
	IMAPUser       string
	IMAPPass       string
	IMAPTLS        string
	DBPath         string
	AttachmentsDir string
	GranteesFile   string
	Folders        []string
	LogLevel       slog.Level
}

type Grantee struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Emails []string `json:"emails"`
}

type GranteesFile struct {
	Grantees []Grantee `json:"grantees"`
}

func FromEnv() (*Config, error) {
	c := &Config{
		IMAPAddr:       os.Getenv("IMAP_ADDR"),
		IMAPUser:       os.Getenv("IMAP_USER"),
		IMAPPass:       os.Getenv("IMAP_PASS"),
		IMAPTLS:        getenvDefault("IMAP_TLS", "starttls"),
		DBPath:         os.Getenv("MAIL_DB_PATH"),
		AttachmentsDir: os.Getenv("MAIL_ATTACHMENTS_DIR"),
		GranteesFile:   os.Getenv("MAIL_GRANTEES_FILE"),
	}

	folders := getenvDefault("MAIL_FOLDERS", "INBOX,Sent")
	for _, f := range strings.Split(folders, ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			c.Folders = append(c.Folders, f)
		}
	}

	switch strings.ToLower(getenvDefault("MAIL_LOG_LEVEL", "info")) {
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
	missing := []string{}
	if c.IMAPAddr == "" {
		missing = append(missing, "IMAP_ADDR")
	}
	if c.IMAPUser == "" {
		missing = append(missing, "IMAP_USER")
	}
	if c.IMAPPass == "" {
		missing = append(missing, "IMAP_PASS")
	}
	if c.DBPath == "" {
		missing = append(missing, "MAIL_DB_PATH")
	}
	if c.AttachmentsDir == "" {
		missing = append(missing, "MAIL_ATTACHMENTS_DIR")
	}
	if c.GranteesFile == "" {
		missing = append(missing, "MAIL_GRANTEES_FILE")
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

func LoadGrantees(path string) ([]Grantee, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read grantees file: %w", err)
	}
	var f GranteesFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse grantees file: %w", err)
	}
	seen := map[string]string{}
	for _, g := range f.Grantees {
		if g.ID == "" {
			return nil, fmt.Errorf("grantee with empty id")
		}
		for _, e := range g.Emails {
			el := strings.ToLower(strings.TrimSpace(e))
			if el == "" {
				continue
			}
			if other, ok := seen[el]; ok && other != g.ID {
				return nil, fmt.Errorf("email %s assigned to multiple grantees (%s, %s)", el, other, g.ID)
			}
			seen[el] = g.ID
		}
	}
	return f.Grantees, nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
