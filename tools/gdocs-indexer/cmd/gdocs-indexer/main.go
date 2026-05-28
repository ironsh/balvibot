// gdocs-indexer mirrors a curated set of Google Docs into SQLite and serves
// them to agents over a read-only MCP endpoint. See the package README for
// the trust model, sync algorithm, and CLI reference.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ironcd/philanthropy-os/tools/gdocs-indexer/internal/config"
	"github.com/ironcd/philanthropy-os/tools/gdocs-indexer/internal/drive"
	"github.com/ironcd/philanthropy-os/tools/gdocs-indexer/internal/mcpserver"
	"github.com/ironcd/philanthropy-os/tools/gdocs-indexer/internal/store"
	"github.com/ironcd/philanthropy-os/tools/gdocs-indexer/internal/sync"
)

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "gdocs-indexer",
		Short:         "Mirror grantee Google Docs into SQLite + MCP",
		Long:          "gdocs-indexer mirrors a curated set of Google Docs into SQLite and serves them to agents over a read-only MCP endpoint. All subcommands read IRON_GDOCS_DB_PATH from the environment.",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(
		newRunCmd(),
		newSyncOnceCmd(),
		newServeMCPCmd(),
		newAuthorizeCmd(),
		newRevokeCmd(),
		newGranteeStatusCmd("pause-grantee", "Mark a grantee paused (skipped by sync).", store.StatusPaused),
		newGranteeStatusCmd("resume-grantee", "Mark a paused grantee active again.", store.StatusActive),
		newListGranteesCmd(),
	)
	return root
}

func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Main daemon: poll loop + MCP server.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.FromEnv()
			if err != nil {
				return err
			}
			logger := setupLogger(cfg.LogLevel)
			logger.Info("starting gdocs-indexer",
				"db", cfg.DBPath,
				"poll_interval", cfg.PollInterval.String(),
				"mcp_enabled", cfg.MCPEnabled,
				"mcp_listen", cfg.MCPListenAddr,
				"iron_proxy_url", cfg.IronProxyURL,
			)

			st, err := store.Open(cfg.DBPath)
			if err != nil {
				return err
			}
			defer st.Close()

			d, err := openDriveClient(cfg)
			if err != nil {
				return err
			}

			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			errs := make(chan error, 2)
			go func() { errs <- runPollLoop(ctx, cfg, st, d, logger) }()
			if cfg.MCPEnabled {
				go func() {
					errs <- mcpserver.Run(ctx, mcpserver.Config{
						BindAddr:    cfg.MCPListenAddr,
						BearerToken: cfg.MCPBearerToken,
					}, st, logger)
				}()
			} else {
				errs <- nil
			}

			var firstErr error
			for i := 0; i < 2; i++ {
				e := <-errs
				if e != nil && !errors.Is(e, context.Canceled) && firstErr == nil {
					firstErr = e
					cancel()
				}
			}
			return firstErr
		},
	}
}

func newSyncOnceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync-once",
		Short: "Run one sync cycle, then exit.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.FromEnv()
			if err != nil {
				return err
			}
			logger := setupLogger(cfg.LogLevel)

			st, err := store.Open(cfg.DBPath)
			if err != nil {
				return err
			}
			defer st.Close()
			d, err := openDriveClient(cfg)
			if err != nil {
				return err
			}

			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()
			_, err = sync.Run(ctx, st, d, logger)
			return err
		},
	}
}

func newServeMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve-mcp",
		Short: "MCP server only (no poll loop).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.FromEnv()
			if err != nil {
				return err
			}
			if !cfg.MCPEnabled {
				return errors.New("IRON_GDOCS_MCP_ENABLED is false; nothing to serve")
			}
			logger := setupLogger(cfg.LogLevel)

			st, err := store.Open(cfg.DBPath)
			if err != nil {
				return err
			}
			defer st.Close()

			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			return mcpserver.Run(ctx, mcpserver.Config{
				BindAddr:    cfg.MCPListenAddr,
				BearerToken: cfg.MCPBearerToken,
			}, st, logger)
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
	var grantee, granteeName string
	cmd := &cobra.Command{
		Use:   name + " <drive-id>",
		Short: fmt.Sprintf("Ingest a Drive %s under a grantee.", name),
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			driveID := strings.TrimSpace(args[0])
			if driveID == "" {
				return errors.New("drive-id cannot be empty")
			}
			st, ctx, err := openStore()
			if err != nil {
				return err
			}
			defer st.Close()

			if err := st.EnsureGrantee(ctx, store.Grantee{
				GranteeID:   grantee,
				DisplayName: granteeName,
				Status:      store.StatusActive,
			}); err != nil {
				return fmt.Errorf("ensure grantee %s: %w", grantee, err)
			}
			if _, err := st.UpsertSource(ctx, store.Source{
				GranteeID:  grantee,
				SourceType: srcType,
				DriveID:    driveID,
			}); err != nil {
				return fmt.Errorf("authorize %s %s: %w", srcType, driveID, err)
			}
			fmt.Printf("ok: %s %s authorized for grantee %s\n", srcType, driveID, grantee)
			return nil
		},
	}
	cmd.Flags().StringVar(&grantee, "grantee", "", "Grantee slug. Created on first authorize.")
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
	var grantee string
	cmd := &cobra.Command{
		Use:   name + " <drive-id>",
		Short: fmt.Sprintf("Stop ingesting a Drive %s for a grantee.", name),
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			driveID := strings.TrimSpace(args[0])
			if driveID == "" {
				return errors.New("drive-id cannot be empty")
			}
			st, ctx, err := openStore()
			if err != nil {
				return err
			}
			defer st.Close()

			if err := st.DeleteSource(ctx, grantee, driveID); err != nil {
				return fmt.Errorf("revoke %s %s from %s: %w", srcType, driveID, grantee, err)
			}
			fmt.Printf("ok: %s %s revoked from grantee %s\n", srcType, driveID, grantee)
			return nil
		},
	}
	cmd.Flags().StringVar(&grantee, "grantee", "", "Grantee slug to revoke from.")
	_ = cmd.MarkFlagRequired("grantee")
	return cmd
}

func newGranteeStatusCmd(use, short, status string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <slug>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			slug := strings.TrimSpace(args[0])
			if slug == "" {
				return errors.New("grantee slug required")
			}
			st, ctx, err := openStore()
			if err != nil {
				return err
			}
			defer st.Close()
			if err := st.SetGranteeStatus(ctx, slug, status); err != nil {
				return fmt.Errorf("set status: %w", err)
			}
			fmt.Printf("ok: grantee %s is now %s\n", slug, status)
			return nil
		},
	}
}

func newListGranteesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list-grantees",
		Short: "Print every grantee and its authorized sources.",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			st, ctx, err := openStore()
			if err != nil {
				return err
			}
			defer st.Close()
			grantees, err := st.ListGrantees(ctx)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "GRANTEE\tSTATUS\tNAME\tSOURCES")
			for _, g := range grantees {
				srcs, err := st.ListSourcesForGrantee(ctx, g.GranteeID)
				if err != nil {
					return err
				}
				parts := make([]string, 0, len(srcs))
				for _, s := range srcs {
					parts = append(parts, s.SourceType+":"+s.DriveID)
				}
				joined := strings.Join(parts, ",")
				if joined == "" {
					joined = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", g.GranteeID, g.Status, g.DisplayName, joined)
			}
			return tw.Flush()
		},
	}
}

func setupLogger(level slog.Level) *slog.Logger {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	return logger
}

func openDriveClient(cfg *config.Config) (*drive.Client, error) {
	return drive.New(drive.Config{
		BaseURL:     cfg.DriveBaseURL,
		BrokerToken: cfg.BrokerToken,
		CAFile:      cfg.CAFile,
	})
}

func runPollLoop(ctx context.Context, cfg *config.Config, st *store.Store, d sync.DriveAPI, logger *slog.Logger) error {
	backoff := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := sync.Run(ctx, st, d, logger)
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

// openStore opens the configured SQLite DB and returns a fresh background
// context. Subcommands close the store via defer.
func openStore() (*store.Store, context.Context, error) {
	dbPath := os.Getenv("IRON_GDOCS_DB_PATH")
	if dbPath == "" {
		return nil, nil, errors.New("IRON_GDOCS_DB_PATH not set")
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, nil, err
	}
	return st, context.Background(), nil
}
