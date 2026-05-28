// gdocs-indexer mirrors a curated set of Google Docs into SQLite and serves
// them to agents over a read-only MCP endpoint. See the package README for
// the trust model, sync algorithm, and CLI reference.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ironcd/philanthropy-os/tools/gdocs-indexer/internal/config"
	"github.com/ironcd/philanthropy-os/tools/gdocs-indexer/internal/drive"
	"github.com/ironcd/philanthropy-os/tools/gdocs-indexer/internal/mcpserver"
	"github.com/ironcd/philanthropy-os/tools/gdocs-indexer/internal/store"
	"github.com/ironcd/philanthropy-os/tools/gdocs-indexer/internal/sync"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	if err := dispatch(cmd, args); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `gdocs-indexer — mirror grantee Google Docs into SQLite + MCP

Usage:
  gdocs-indexer run               Main daemon: poll loop + MCP server.
  gdocs-indexer sync-once         Run one sync cycle, then exit.
  gdocs-indexer serve-mcp         MCP server only (no poll loop).
  gdocs-indexer register-grantee  Insert/update a grantee + optional sources.
  gdocs-indexer add-source        Attach a folder or doc source to an existing grantee.
  gdocs-indexer list-grantees     Print every grantee and its registered sources.

All commands read IRON_GDOCS_DB_PATH from the environment.`)
}

func dispatch(cmd string, args []string) error {
	switch cmd {
	case "run":
		return cmdRun(args)
	case "sync-once":
		return cmdSyncOnce(args)
	case "serve-mcp":
		return cmdServeMCP(args)
	case "register-grantee":
		return cmdRegisterGrantee(args)
	case "add-source":
		return cmdAddSource(args)
	case "list-grantees":
		return cmdListGrantees(args)
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q (try `gdocs-indexer help`)", cmd)
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

// cmdRun is the daemon: poll loop + MCP server in one process.
func cmdRun(args []string) error {
	_ = parseEmptyFlags("run", args)

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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	errs := make(chan error, 2)

	go func() {
		errs <- runPollLoop(ctx, cfg, st, d, logger)
	}()

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

	// Wait for both goroutines. First non-context error wins.
	var firstErr error
	for i := 0; i < 2; i++ {
		e := <-errs
		if e != nil && !errors.Is(e, context.Canceled) && firstErr == nil {
			firstErr = e
			cancel()
		}
	}
	return firstErr
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

func cmdSyncOnce(args []string) error {
	_ = parseEmptyFlags("sync-once", args)
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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	_, err = sync.Run(ctx, st, d, logger)
	return err
}

func cmdServeMCP(args []string) error {
	_ = parseEmptyFlags("serve-mcp", args)
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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return mcpserver.Run(ctx, mcpserver.Config{
		BindAddr:    cfg.MCPListenAddr,
		BearerToken: cfg.MCPBearerToken,
	}, st, logger)
}

func cmdRegisterGrantee(args []string) error {
	fs := flag.NewFlagSet("register-grantee", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	id := fs.String("id", "", "Grantee id (slug). Required.")
	owner := fs.String("owner-email", "", "Owner email whose docs count as this grantee's. Required.")
	display := fs.String("display-name", "", "Human-readable name.")
	folders := stringSliceFlag{}
	docs := stringSliceFlag{}
	fs.Var(&folders, "folder", "Drive folder id to attach (may repeat).")
	fs.Var(&docs, "doc", "Drive doc id to attach (may repeat).")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" || *owner == "" {
		fs.Usage()
		return errors.New("--id and --owner-email are required")
	}

	dbPath := os.Getenv("IRON_GDOCS_DB_PATH")
	if dbPath == "" {
		return errors.New("IRON_GDOCS_DB_PATH not set")
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	if err := st.UpsertGrantee(ctx, store.Grantee{
		GranteeID:   *id,
		OwnerEmail:  *owner,
		DisplayName: *display,
		Status:      store.StatusActive,
	}); err != nil {
		return fmt.Errorf("upsert grantee: %w", err)
	}
	for _, f := range folders {
		if _, err := st.UpsertSource(ctx, store.Source{
			GranteeID: *id, SourceType: store.SourceTypeFolder, DriveID: strings.TrimSpace(f),
		}); err != nil {
			return fmt.Errorf("attach folder %s: %w", f, err)
		}
	}
	for _, d := range docs {
		if _, err := st.UpsertSource(ctx, store.Source{
			GranteeID: *id, SourceType: store.SourceTypeDoc, DriveID: strings.TrimSpace(d),
		}); err != nil {
			return fmt.Errorf("attach doc %s: %w", d, err)
		}
	}
	fmt.Printf("ok: grantee %s registered (owner=%s, folders=%d, docs=%d)\n", *id, *owner, len(folders), len(docs))
	return nil
}

func cmdAddSource(args []string) error {
	fs := flag.NewFlagSet("add-source", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	grantee := fs.String("grantee", "", "Existing grantee id. Required.")
	folder := fs.String("folder", "", "Drive folder id.")
	doc := fs.String("doc", "", "Drive doc id.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *grantee == "" || (*folder == "" && *doc == "") || (*folder != "" && *doc != "") {
		fs.Usage()
		return errors.New("--grantee plus exactly one of --folder or --doc is required")
	}

	dbPath := os.Getenv("IRON_GDOCS_DB_PATH")
	if dbPath == "" {
		return errors.New("IRON_GDOCS_DB_PATH not set")
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	if _, err := st.GetGrantee(ctx, *grantee); err != nil {
		return fmt.Errorf("grantee %s: %w", *grantee, err)
	}
	src := store.Source{GranteeID: *grantee}
	if *folder != "" {
		src.SourceType = store.SourceTypeFolder
		src.DriveID = *folder
	} else {
		src.SourceType = store.SourceTypeDoc
		src.DriveID = *doc
	}
	if _, err := st.UpsertSource(ctx, src); err != nil {
		return err
	}
	fmt.Printf("ok: %s source %s attached to grantee %s\n", src.SourceType, src.DriveID, src.GranteeID)
	return nil
}

func cmdListGrantees(args []string) error {
	_ = parseEmptyFlags("list-grantees", args)
	dbPath := os.Getenv("IRON_GDOCS_DB_PATH")
	if dbPath == "" {
		return errors.New("IRON_GDOCS_DB_PATH not set")
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	grantees, err := st.ListGrantees(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "GRANTEE\tSTATUS\tOWNER\tNAME\tSOURCES")
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
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", g.GranteeID, g.Status, g.OwnerEmail, g.DisplayName, joined)
	}
	return tw.Flush()
}

// parseEmptyFlags rejects unknown args on subcommands that take none.
func parseEmptyFlags(name string, args []string) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs.Parse(args)
}

// stringSliceFlag is a flag.Value that collects repeated --flag values.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(*s, ",")
}

func (s *stringSliceFlag) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return errors.New("empty value")
	}
	*s = append(*s, v)
	return nil
}
