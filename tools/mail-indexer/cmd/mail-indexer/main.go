package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/ironcd/philanthropy-os/tools/mail-indexer/internal/cas"
	"github.com/ironcd/philanthropy-os/tools/mail-indexer/internal/config"
	"github.com/ironcd/philanthropy-os/tools/mail-indexer/internal/grantee"
	"github.com/ironcd/philanthropy-os/tools/mail-indexer/internal/mailbox"
	"github.com/ironcd/philanthropy-os/tools/mail-indexer/internal/store"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	logger.Info("starting mail-indexer",
		"imap_addr", cfg.IMAPAddr,
		"db", cfg.DBPath,
		"attachments", cfg.AttachmentsDir,
		"folders", cfg.Folders,
	)

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	casStore, err := cas.New(cfg.AttachmentsDir)
	if err != nil {
		return err
	}

	if err := reloadGrantees(context.Background(), st, cfg.GranteesFile, logger); err != nil {
		return err
	}

	resolver := grantee.NewResolver(st)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				logger.Info("SIGHUP: reloading grantees")
				if err := reloadGrantees(ctx, st, cfg.GranteesFile, logger); err != nil {
					logger.Error("reload grantees failed", "err", err)
				}
			}
		}
	}()

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
}

func reloadGrantees(ctx context.Context, st *store.Store, path string, logger *slog.Logger) error {
	grantees, err := config.LoadGrantees(path)
	if err != nil {
		return err
	}
	if err := st.ReconcileGrantees(ctx, grantees); err != nil {
		return err
	}
	logger.Info("grantees loaded", "count", len(grantees))
	return nil
}
