// Command api is the consolidated balvibot backend: a single binary
// that, via subcommands, runs the MCP server (serve), the IMAP mail indexer
// (index-mail), the Google Docs sync loop (sync-gdocs), the Goose migrations
// (migrate), and the grantee-admin CLI (grantee ...). All subcommands share
// one Postgres database (DATABASE_URL).
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2/google"

	"github.com/ironsh/balvibot/tools/api/internal/actions"
	"github.com/ironsh/balvibot/tools/api/internal/approval"
	"github.com/ironsh/balvibot/tools/api/internal/approvalserver"
	"github.com/ironsh/balvibot/tools/api/internal/cas"
	"github.com/ironsh/balvibot/tools/api/internal/config"
	"github.com/ironsh/balvibot/tools/api/internal/db"
	"github.com/ironsh/balvibot/tools/api/internal/drive"
	"github.com/ironsh/balvibot/tools/api/internal/grantee"
	"github.com/ironsh/balvibot/tools/api/internal/mailbox"
	"github.com/ironsh/balvibot/tools/api/internal/mcpserver"
	"github.com/ironsh/balvibot/tools/api/internal/store"
	gdocssync "github.com/ironsh/balvibot/tools/api/internal/sync"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "api",
		Short:         "balvibot consolidated backend (MCP + mail/docs indexers + grantee admin)",
		Long:          "api is the single Postgres-backed service for balvibot. Subcommands share DATABASE_URL.",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(
		newMigrateCmd(),
		newServeCmd(),
		newApproveServeCmd(),
		newApproveUserCmd(),
		newIndexMailCmd(),
		newSyncGdocsCmd(),
		newGranteeCmd(),
	)
	return root
}

func setupLogger(level slog.Level) *slog.Logger {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	return logger
}

// openStore loads config, validates the DB DSN, and opens a *store.Store.
// Migrations are NOT run here; use `api migrate up`.
func openStore(ctx context.Context) (*store.Store, *config.Config, error) {
	cfg, err := config.FromEnv()
	if err != nil {
		return nil, nil, err
	}
	if err := cfg.RequireDB(); err != nil {
		return nil, nil, err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	return store.New(pool), cfg, nil
}

// bootstrapApprovalUser seeds the initial approval operator from
// APPROVAL_BOOTSTRAP_EMAIL + APPROVAL_BOOTSTRAP_PUBKEY (an authorized_keys
// line). The fingerprint is derived from the key. It runs after `api migrate
// up` so a fresh deployment has one authorized approver without a manual step;
// it is an idempotent upsert, so re-running migrations keeps the key current.
// No-op when neither env var is set.
func bootstrapApprovalUser(ctx context.Context, d *sql.DB) error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	email := cfg.ApprovalBootstrapEmail
	pubKey := cfg.ApprovalBootstrapPubKey
	if email == "" && pubKey == "" {
		return nil
	}
	if email == "" || pubKey == "" {
		return errors.New("APPROVAL_BOOTSTRAP_EMAIL and APPROVAL_BOOTSTRAP_PUBKEY must be set together")
	}
	fp, err := approval.Fingerprint(pubKey)
	if err != nil {
		return fmt.Errorf("bootstrap pubkey: %w", err)
	}
	if err := store.New(d).UpsertApprovalUser(ctx, email, pubKey, fp); err != nil {
		return fmt.Errorf("bootstrap approval user: %w", err)
	}
	fmt.Printf("ok: bootstrapped approval user %s (%s)\n", email, fp)
	return nil
}

// ---------- migrate ----------

func newMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Run Goose database migrations.",
	}
	open := func() (*sql.DB, error) {
		cfg, err := config.FromEnv()
		if err != nil {
			return nil, err
		}
		if err := cfg.RequireDB(); err != nil {
			return nil, err
		}
		return db.Open(context.Background(), cfg.DatabaseURL)
	}
	cmd.AddCommand(
		&cobra.Command{
			Use: "up", Short: "Apply all pending migrations, then seed the bootstrap approval user.", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				d, err := open()
				if err != nil {
					return err
				}
				defer d.Close()
				if err := db.MigrateUp(d); err != nil {
					return err
				}
				return bootstrapApprovalUser(cmd.Context(), d)
			},
		},
		&cobra.Command{
			Use: "down", Short: "Roll back the most recent migration.", Args: cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				d, err := open()
				if err != nil {
					return err
				}
				defer d.Close()
				return db.MigrateDown(d)
			},
		},
		&cobra.Command{
			Use: "status", Short: "Print migration status.", Args: cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				d, err := open()
				if err != nil {
					return err
				}
				defer d.Close()
				return db.MigrateStatus(d)
			},
		},
	)
	return cmd
}

// ---------- serve (MCP) ----------

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the MCP server (grantees + mail + docs tools).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.FromEnv()
			if err != nil {
				return err
			}
			if err := cfg.RequireMCP(); err != nil {
				return err
			}
			logger := setupLogger(cfg.LogLevel)

			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			pool, err := db.Open(ctx, cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer pool.Close()
			st := store.New(pool)

			// whitelist_doc resolves a Drive id's folder-vs-doc type at enqueue
			// time, so the MCP server needs read-only Drive access. As a
			// trusted backend (not an agent workload), it holds the
			// service-account credential and talks to Drive directly. If the
			// credential is missing or unreadable we still start: every other
			// tool is Drive-free and whitelist_doc fails loudly when called.
			var d mcpserver.DriveLookup
			if drv, err := newDriveClient(ctx, cfg); err != nil {
				logger.Warn("drive client unavailable; whitelist_doc will fail until configured", "err", err)
			} else {
				d = drv
			}

			logger.Info("starting api serve", "mcp_bind", cfg.MCPBindAddr)
			return mcpserver.Run(ctx, mcpserver.Config{
				BindAddr:    cfg.MCPBindAddr,
				BearerToken: cfg.MCPBearerToken,
			}, st, d, logger)
		},
	}
}

// driveReadonlyScope is all whitelist_doc needs: files.get metadata to tell a
// folder from a Google Doc.
const driveReadonlyScope = "https://www.googleapis.com/auth/drive.readonly"

// newDriveClient builds a Drive client that authenticates directly with the
// service-account key at cfg.GoogleSAKeyFile (no iron-proxy: the MCP server is
// a trusted backend that holds the credential itself).
func newDriveClient(ctx context.Context, cfg *config.Config) (*drive.Client, error) {
	if cfg.GoogleSAKeyFile == "" {
		return nil, errors.New("GOOGLE_APPLICATION_CREDENTIALS not set")
	}
	keyJSON, err := os.ReadFile(cfg.GoogleSAKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read service-account key: %w", err)
	}
	creds, err := google.CredentialsFromJSON(ctx, keyJSON, driveReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("parse service-account key: %w", err)
	}
	return drive.New(drive.Config{
		BaseURL:     cfg.DriveBaseURL,
		TokenSource: creds.TokenSource,
	})
}

// ---------- approve-serve (approval service) ----------

func newApproveServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "approve-serve",
		Short: "Run the approval service (MCP enqueue endpoint + operator approval API).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.FromEnv()
			if err != nil {
				return err
			}
			if err := cfg.RequireApproval(); err != nil {
				return err
			}
			logger := setupLogger(cfg.LogLevel)

			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			pool, err := db.Open(ctx, cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer pool.Close()
			st := store.New(pool)

			// Executor: the real handlers for each approval-gated action. The
			// MCP tools on the api server enqueue these; they run here only
			// after an operator's signature is verified. The executors are
			// Drive-free — whitelist_doc's folder-vs-doc classification is
			// resolved at enqueue time by the MCP server, so this service
			// needs no iron-proxy egress.
			registry := approval.NewRegistry()
			actions.Register(registry, st)

			logger.Info("starting api approve-serve", "approval_bind", cfg.ApprovalBindAddr)
			return approvalserver.Run(ctx, approvalserver.Config{
				BindAddr: cfg.ApprovalBindAddr,
			}, st, registry, logger)
		},
	}
}

// ---------- approve-user admin ----------

func newApproveUserCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "approve-user",
		Short: "Manage operators authorized to approve actions (email + SSH public key).",
	}
	cmd.AddCommand(
		newApproveUserAddCmd(),
		newApproveUserListCmd(),
		newApproveUserRemoveCmd(),
	)
	return cmd
}

func newApproveUserAddCmd() *cobra.Command {
	var keyFile string
	cmd := &cobra.Command{
		Use:   "add <email>",
		Short: "Authorize an operator by email and SSH public key (upsert).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email := strings.TrimSpace(args[0])
			if email == "" {
				return errors.New("email required")
			}
			if keyFile == "" {
				return errors.New("--key-file is required (path to an SSH public key)")
			}
			raw, err := os.ReadFile(keyFile)
			if err != nil {
				return fmt.Errorf("read key file: %w", err)
			}
			pubKey := strings.TrimSpace(string(raw))
			fp, err := approval.Fingerprint(pubKey)
			if err != nil {
				return err
			}
			st, _, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			if err := st.UpsertApprovalUser(cmd.Context(), email, pubKey, fp); err != nil {
				return err
			}
			fmt.Printf("ok: %s authorized (%s)\n", email, fp)
			return nil
		},
	}
	cmd.Flags().StringVar(&keyFile, "key-file", "", "Path to the operator's SSH public key (authorized_keys format).")
	_ = cmd.MarkFlagRequired("key-file")
	return cmd
}

func newApproveUserListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List authorized operators.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, _, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			users, err := st.ListApprovalUsers(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "EMAIL\tFINGERPRINT")
			for _, u := range users {
				fmt.Fprintf(tw, "%s\t%s\n", u.Email, u.Fingerprint)
			}
			return tw.Flush()
		},
	}
}

func newApproveUserRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <email>",
		Short: "Revoke an operator's approval authorization.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email := strings.TrimSpace(args[0])
			if email == "" {
				return errors.New("email required")
			}
			st, _, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			if err := st.RemoveApprovalUser(cmd.Context(), email); err != nil {
				return err
			}
			fmt.Printf("ok: %s revoked\n", email)
			return nil
		},
	}
}

// ---------- index-mail ----------

func newIndexMailCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "index-mail",
		Short: "Run the IMAP mail indexer (one worker per folder).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.FromEnv()
			if err != nil {
				return err
			}
			if err := cfg.RequireMail(); err != nil {
				return err
			}
			logger := setupLogger(cfg.LogLevel)
			logger.Info("starting mail indexer",
				"imap_addr", cfg.IMAPAddr,
				"attachments", cfg.AttachmentsDir,
				"folders", cfg.Folders,
			)

			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			pool, err := db.Open(ctx, cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer pool.Close()
			st := store.New(pool)

			casStore, err := cas.New(cfg.AttachmentsDir)
			if err != nil {
				return err
			}
			resolver := grantee.NewResolver(st)

			var wg sync.WaitGroup
			errs := make(chan error, len(cfg.Folders))
			for _, f := range cfg.Folders {
				wg.Add(1)
				go func(folder string) {
					defer wg.Done()
					ix := mailbox.NewIndexer(cfg, st, casStore, resolver, folder, logger)
					if err := ix.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
						errs <- err
					}
				}(f)
			}
			wg.Wait()
			close(errs)
			for e := range errs {
				if e != nil {
					return e
				}
			}
			return ctx.Err()
		},
	}
}

// ---------- sync-gdocs ----------

func newSyncGdocsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync-gdocs",
		Short: "Run the Google Docs sync poll loop.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.FromEnv()
			if err != nil {
				return err
			}
			if err := cfg.RequireGdocs(); err != nil {
				return err
			}
			logger := setupLogger(cfg.LogLevel)
			logger.Info("starting gdocs sync",
				"poll_interval", cfg.PollInterval.String(),
			)

			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			pool, err := db.Open(ctx, cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer pool.Close()
			st := store.New(pool)

			d, err := newDriveClient(ctx, cfg)
			if err != nil {
				return err
			}
			return runPollLoop(ctx, cfg, st, d, logger)
		},
	}
}

func runPollLoop(ctx context.Context, cfg *config.Config, st *store.Store, d gdocssync.DriveAPI, logger *slog.Logger) error {
	backoff := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := gdocssync.Run(ctx, st, d, logger)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			logger.Error("sync cycle failed", "err", err, "retry_in", backoff.String())
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > 60*time.Second {
				backoff = 60 * time.Second
			}
			continue
		}
		backoff = time.Second
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cfg.PollInterval):
		}
	}
}

// ---------- grantee admin ----------

func newGranteeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "grantee",
		Short: "Manage grantees, their emails, and their authorized Drive sources.",
	}
	cmd.AddCommand(
		newGranteeCreateCmd(),
		newGranteeListCmd(),
		newGranteeStatusCmd("pause", "Mark a grantee paused (skipped by gdocs sync).", store.StatusPaused),
		newGranteeStatusCmd("resume", "Mark a paused grantee active again.", store.StatusActive),
		newGranteeEmailCmd("add-email", "Map a sender email to a grantee (mail attribution).", true),
		newGranteeEmailCmd("remove-email", "Unmap a sender email from a grantee.", false),
		newAuthorizeCmd(),
		newRevokeCmd(),
	)
	return cmd
}

func newGranteeCreateCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "create <slug>",
		Short: "Create a grantee (no-op if it already exists).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := strings.TrimSpace(args[0])
			if slug == "" {
				return errors.New("grantee slug required")
			}
			st, _, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			if err := st.EnsureGrantee(cmd.Context(), store.Grantee{
				GranteeID:   slug,
				DisplayName: name,
				Status:      store.StatusActive,
			}); err != nil {
				return err
			}
			fmt.Printf("ok: grantee %s ready\n", slug)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Display name (used only when first created).")
	return cmd
}

func newGranteeListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Print every grantee with its status, emails, and authorized sources.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, _, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			grantees, err := st.ListGrantees(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "GRANTEE\tSTATUS\tNAME\tEMAILS\tSOURCES")
			for _, g := range grantees {
				srcs, err := st.ListSourcesForGrantee(cmd.Context(), g.GranteeID)
				if err != nil {
					return err
				}
				parts := make([]string, 0, len(srcs))
				for _, s := range srcs {
					parts = append(parts, s.SourceType+":"+s.DriveID)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					g.GranteeID, g.Status, g.DisplayName,
					dashIfEmpty(strings.Join(g.Emails, ",")),
					dashIfEmpty(strings.Join(parts, ",")),
				)
			}
			return tw.Flush()
		},
	}
}

func newGranteeStatusCmd(use, short, status string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <slug>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := strings.TrimSpace(args[0])
			if slug == "" {
				return errors.New("grantee slug required")
			}
			st, _, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			if err := st.SetGranteeStatus(cmd.Context(), slug, status); err != nil {
				return fmt.Errorf("set status: %w", err)
			}
			fmt.Printf("ok: grantee %s is now %s\n", slug, status)
			return nil
		},
	}
}

func newGranteeEmailCmd(use, short string, add bool) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <slug> <email>",
		Short: short,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := strings.TrimSpace(args[0])
			email := strings.TrimSpace(args[1])
			if slug == "" || email == "" {
				return errors.New("grantee slug and email required")
			}
			st, _, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			if add {
				if err := st.EnsureGrantee(cmd.Context(), store.Grantee{GranteeID: slug, Status: store.StatusActive}); err != nil {
					return fmt.Errorf("ensure grantee %s: %w", slug, err)
				}
				if err := st.AddGranteeEmail(cmd.Context(), slug, email); err != nil {
					return err
				}
				fmt.Printf("ok: %s mapped to grantee %s\n", email, slug)
				return nil
			}
			if err := st.RemoveGranteeEmail(cmd.Context(), slug, email); err != nil {
				return err
			}
			fmt.Printf("ok: %s unmapped from grantee %s\n", email, slug)
			return nil
		},
	}
}

func newAuthorizeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "authorize",
		Short: "Authorize a Drive folder or doc for a grantee. Creates the grantee on first use.",
	}
	cmd.AddCommand(
		newAuthorizeSourceCmd("folder", store.SourceTypeFolder),
		newAuthorizeSourceCmd("doc", store.SourceTypeDoc),
	)
	return cmd
}

func newAuthorizeSourceCmd(name, srcType string) *cobra.Command {
	var granteeSlug, granteeName string
	cmd := &cobra.Command{
		Use:   name + " <drive-id>",
		Short: fmt.Sprintf("Ingest a Drive %s under a grantee.", name),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			driveID := strings.TrimSpace(args[0])
			if driveID == "" {
				return errors.New("drive-id cannot be empty")
			}
			st, _, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			if err := st.EnsureGrantee(cmd.Context(), store.Grantee{
				GranteeID:   granteeSlug,
				DisplayName: granteeName,
				Status:      store.StatusActive,
			}); err != nil {
				return fmt.Errorf("ensure grantee %s: %w", granteeSlug, err)
			}
			if _, err := st.UpsertSource(cmd.Context(), store.Source{
				GranteeID:  granteeSlug,
				SourceType: srcType,
				DriveID:    driveID,
			}); err != nil {
				return fmt.Errorf("authorize %s %s: %w", srcType, driveID, err)
			}
			fmt.Printf("ok: %s %s authorized for grantee %s\n", srcType, driveID, granteeSlug)
			return nil
		},
	}
	cmd.Flags().StringVar(&granteeSlug, "grantee", "", "Grantee slug. Created on first authorize.")
	cmd.Flags().StringVar(&granteeName, "grantee-name", "", "Display name (used only when the grantee is first created).")
	_ = cmd.MarkFlagRequired("grantee")
	return cmd
}

func newRevokeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Stop ingesting a Drive folder or doc for a grantee.",
	}
	cmd.AddCommand(
		newRevokeSourceCmd("folder", store.SourceTypeFolder),
		newRevokeSourceCmd("doc", store.SourceTypeDoc),
	)
	return cmd
}

func newRevokeSourceCmd(name, srcType string) *cobra.Command {
	var granteeSlug string
	cmd := &cobra.Command{
		Use:   name + " <drive-id>",
		Short: fmt.Sprintf("Stop ingesting a Drive %s for a grantee.", name),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			driveID := strings.TrimSpace(args[0])
			if driveID == "" {
				return errors.New("drive-id cannot be empty")
			}
			st, _, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			if err := st.DeleteSource(cmd.Context(), granteeSlug, driveID); err != nil {
				return fmt.Errorf("revoke %s %s from %s: %w", srcType, driveID, granteeSlug, err)
			}
			fmt.Printf("ok: %s %s revoked from grantee %s\n", srcType, driveID, granteeSlug)
			return nil
		},
	}
	cmd.Flags().StringVar(&granteeSlug, "grantee", "", "Grantee slug to revoke from.")
	_ = cmd.MarkFlagRequired("grantee")
	return cmd
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
