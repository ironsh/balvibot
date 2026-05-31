// Command api is the consolidated philanthropy-os backend: a single binary
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
		Short:         "philanthropy-os consolidated backend (MCP + mail/docs indexers + grantee admin)",
		Long:          "api is the single Postgres-backed service for philanthropy-os. Subcommands share DATABASE_URL.",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(
		newMigrateCmd(),
		newServeCmd(),
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
			Use: "up", Short: "Apply all pending migrations.", Args: cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				d, err := open()
				if err != nil {
					return err
				}
				defer d.Close()
				return db.MigrateUp(d)
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

			logger.Info("starting api serve", "mcp_bind", cfg.MCPBindAddr)
			return mcpserver.Run(ctx, mcpserver.Config{
				BindAddr:    cfg.MCPBindAddr,
				BearerToken: cfg.MCPBearerToken,
			}, st, logger)
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
				"iron_proxy_url", cfg.IronProxyURL,
			)

			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			pool, err := db.Open(ctx, cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer pool.Close()
			st := store.New(pool)

			d, err := drive.New(drive.Config{
				BaseURL:     cfg.DriveBaseURL,
				BrokerToken: cfg.BrokerToken,
				CAFile:      cfg.CAFile,
			})
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
