package mailbox

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/ironsh/balvibot/tools/api/internal/cas"
	"github.com/ironsh/balvibot/tools/api/internal/config"
	"github.com/ironsh/balvibot/tools/api/internal/grantee"
	"github.com/ironsh/balvibot/tools/api/internal/parse"
	"github.com/ironsh/balvibot/tools/api/internal/store"
)

type Indexer struct {
	cfg      *config.Config
	store    *store.Store
	cas      *cas.Store
	resolver *grantee.Resolver
	folder   string
	logger   *slog.Logger
}

func NewIndexer(cfg *config.Config, st *store.Store, casStore *cas.Store, r *grantee.Resolver, folder string, logger *slog.Logger) *Indexer {
	return &Indexer{
		cfg:      cfg,
		store:    st,
		cas:      casStore,
		resolver: r,
		folder:   folder,
		logger:   logger.With("folder", folder),
	}
}

// Run connects, syncs, and idles until ctx is cancelled. Reconnects with
// backoff on error.
func (ix *Indexer) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := ix.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			ix.logger.Error("session ended", "err", err, "retry_in", backoff.String())
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
	}
}

func (ix *Indexer) runOnce(ctx context.Context) error {
	notify := make(chan struct{}, 8)
	c, err := ix.dial(notify)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer c.Close()

	if err := c.Login(ix.cfg.IMAPUser, ix.cfg.IMAPPass).Wait(); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	ix.logger.Info("logged in")

	sel, err := c.Select(ix.folder, nil).Wait()
	if err != nil {
		return fmt.Errorf("select %s: %w", ix.folder, err)
	}
	uidValidity := sel.UIDValidity
	ix.logger.Info("selected", "uid_validity", uidValidity, "num_messages", sel.NumMessages)

	if err := ix.sync(ctx, c, uidValidity); err != nil {
		return fmt.Errorf("initial sync: %w", err)
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		changed, err := ix.idleUntilChange(ctx, c, notify)
		if err != nil {
			return err
		}
		if !changed {
			continue
		}
		if err := ix.sync(ctx, c, uidValidity); err != nil {
			return fmt.Errorf("incremental sync: %w", err)
		}
	}
}

func (ix *Indexer) dial(notify chan<- struct{}) (*imapclient.Client, error) {
	opts := &imapclient.Options{
		TLSConfig: &tls.Config{InsecureSkipVerify: true},
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(data *imapclient.UnilateralDataMailbox) {
				if data != nil && data.NumMessages != nil {
					select {
					case notify <- struct{}{}:
					default:
					}
				}
			},
		},
	}
	switch ix.cfg.IMAPTLS {
	case "tls":
		return imapclient.DialTLS(ix.cfg.IMAPAddr, opts)
	case "none":
		return imapclient.DialInsecure(ix.cfg.IMAPAddr, opts)
	default:
		return imapclient.DialStartTLS(ix.cfg.IMAPAddr, opts)
	}
}

func (ix *Indexer) sync(ctx context.Context, c *imapclient.Client, uidValidity uint32) error {
	state, err := ix.store.GetMailboxState(ctx, ix.folder)
	if err != nil {
		return err
	}
	var lastUID uint32
	if state != nil {
		if state.UIDValidity != uidValidity {
			ix.logger.Warn("uid_validity changed, resetting",
				"prev", state.UIDValidity, "new", uidValidity)
			lastUID = 0
		} else {
			lastUID = state.LastUID
		}
	}

	var uidSet imap.UIDSet
	uidSet.AddRange(imap.UID(lastUID+1), 0)

	bodySection := &imap.FetchItemBodySection{Peek: true}
	fetchOpts := &imap.FetchOptions{
		UID:          true,
		Envelope:     true,
		InternalDate: true,
		RFC822Size:   true,
		BodySection:  []*imap.FetchItemBodySection{bodySection},
	}

	cmd := c.Fetch(uidSet, fetchOpts)

	highest := lastUID
	count := 0
	for {
		md := cmd.Next()
		if md == nil {
			break
		}
		buf, err := md.Collect()
		if err != nil {
			cmd.Close()
			return fmt.Errorf("collect message: %w", err)
		}
		uid := uint32(buf.UID)
		if uid == 0 {
			ix.logger.Warn("fetched message without UID, skipping")
			continue
		}
		if err := ix.handleMessage(ctx, buf, uidValidity, bodySection); err != nil {
			ix.logger.Error("index message failed", "uid", uid, "err", err)
			continue
		}
		if uid > highest {
			highest = uid
		}
		count++
	}
	if err := cmd.Close(); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("fetch close: %w", err)
	}

	if highest != lastUID || (state != nil && state.UIDValidity != uidValidity) || state == nil {
		if err := ix.store.SaveMailboxState(ctx, &store.MailboxState{
			Folder:      ix.folder,
			UIDValidity: uidValidity,
			LastUID:     highest,
		}); err != nil {
			return fmt.Errorf("save mailbox state: %w", err)
		}
	}
	if count > 0 {
		ix.logger.Info("synced", "indexed", count, "last_uid", highest)
	}
	return nil
}

func (ix *Indexer) handleMessage(ctx context.Context, buf *imapclient.FetchMessageBuffer, uidValidity uint32, section *imap.FetchItemBodySection) error {
	raw := buf.FindBodySection(section)
	if len(raw) == 0 {
		return errors.New("empty body section")
	}

	if buf.Envelope != nil && buf.Envelope.MessageID != "" {
		if exists, err := ix.store.MessageExistsByID(ctx, buf.Envelope.MessageID); err != nil {
			return err
		} else if exists {
			return nil
		}
	}

	parsed, err := parse.ParseRFC822(raw, ix.cas)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}

	cands := parse.ThreadCandidates(parsed.MessageID, parsed.InReplyTo, parsed.References)
	threadID, err := ix.store.FindThreadByRefs(ctx, cands)
	if err != nil {
		return fmt.Errorf("thread lookup: %w", err)
	}
	if threadID == 0 {
		root := parsed.MessageID
		if len(parsed.RefsList) > 0 {
			root = parsed.RefsList[0]
		} else if irt := parse.ExtractIDs(parsed.InReplyTo); len(irt) > 0 {
			root = irt[0]
		}
		threadID, err = ix.store.CreateThread(ctx, root, parse.NormalizeSubject(parsed.Subject), nil, parsed.Date)
		if err != nil {
			if store.IsUniqueViolation(err) {
				threadID, err = ix.store.FindThreadByRefs(ctx, []string{root})
				if err != nil || threadID == 0 {
					return fmt.Errorf("thread create race: %w", err)
				}
			} else {
				return fmt.Errorf("create thread: %w", err)
			}
		}
	}

	res, err := ix.resolver.Resolve(ctx, parsed.From.Email, threadID)
	if err != nil {
		return fmt.Errorf("resolve grantee: %w", err)
	}

	msg := &store.Message{
		MessageID:   parsed.MessageID,
		ThreadID:    threadID,
		GranteeID:   res.GranteeID,
		Folder:      ix.folder,
		UID:         uint32(buf.UID),
		UIDValidity: uidValidity,
		InReplyTo:   parsed.InReplyTo,
		References:  parsed.RefsList,
		From:        parsed.From,
		To:          parsed.To,
		Cc:          parsed.Cc,
		Bcc:         parsed.Bcc,
		Subject:     parsed.Subject,
		Date:        parsed.Date,
		BodyText:    parsed.BodyText,
		BodyHTML:    parsed.BodyHTML,
		SizeBytes:   buf.RFC822Size,
		Attachments: parsed.Attachments,
	}
	if _, _, err := ix.store.InsertMessage(ctx, msg); err != nil {
		return fmt.Errorf("insert: %w", err)
	}

	if res.FromSender && res.GranteeID != nil {
		if err := ix.resolver.PromoteThread(ctx, threadID, *res.GranteeID); err != nil {
			return fmt.Errorf("promote thread: %w", err)
		}
	}
	if err := ix.store.TouchThread(ctx, threadID, parsed.Date); err != nil {
		return fmt.Errorf("touch thread: %w", err)
	}
	return nil
}

// idleUntilChange enters IDLE and returns (true, nil) when the server signals
// new messages, or (false, nil) on the periodic refresh (caller re-enters).
func (ix *Indexer) idleUntilChange(ctx context.Context, c *imapclient.Client, notify <-chan struct{}) (bool, error) {
	// Drain stale notifications so we only react to fresh ones.
	for {
		select {
		case <-notify:
		default:
			goto drained
		}
	}
drained:

	idleCmd, err := c.Idle()
	if err != nil {
		return false, fmt.Errorf("idle start: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- idleCmd.Wait() }()

	refresh := time.NewTimer(20 * time.Minute)
	defer refresh.Stop()

	stop := func() error {
		if err := idleCmd.Close(); err != nil {
			return fmt.Errorf("idle close: %w", err)
		}
		if err := <-done; err != nil {
			return fmt.Errorf("idle wait: %w", err)
		}
		return nil
	}

	select {
	case <-ctx.Done():
		_ = stop()
		return false, ctx.Err()
	case <-notify:
		if err := stop(); err != nil {
			return false, err
		}
		return true, nil
	case <-refresh.C:
		if err := stop(); err != nil {
			return false, err
		}
		return false, nil
	case err := <-done:
		if err != nil {
			return false, fmt.Errorf("idle wait: %w", err)
		}
		return true, nil
	}
}
