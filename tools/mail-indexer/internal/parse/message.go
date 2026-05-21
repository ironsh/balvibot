package parse

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/mail"
	"strings"
	"time"

	gomail "github.com/emersion/go-message/mail"

	"github.com/ironcd/philanthropy-os/tools/mail-indexer/internal/cas"
	"github.com/ironcd/philanthropy-os/tools/mail-indexer/internal/store"
)

type Parsed struct {
	MessageID   string
	InReplyTo   string
	References  string
	RefsList    []string
	From        store.Address
	To          []store.Address
	Cc          []store.Address
	Bcc         []store.Address
	Subject     string
	Date        time.Time
	BodyText    string
	BodyHTML    string
	Attachments []store.Attachment
}

// ParseRFC822 reads a raw message and decomposes it into a Parsed struct.
// Attachments are streamed into the provided CAS store.
func ParseRFC822(raw []byte, casStore *cas.Store) (*Parsed, error) {
	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("create mail reader: %w", err)
	}

	p := &Parsed{}
	h := mr.Header

	p.MessageID, _ = h.MessageID()
	p.InReplyTo = strings.TrimSpace(h.Get("In-Reply-To"))
	p.References = strings.TrimSpace(h.Get("References"))
	p.RefsList = ExtractIDs(p.References)

	if subj, err := h.Subject(); err == nil {
		p.Subject = subj
	}
	if d, err := h.Date(); err == nil {
		p.Date = d
	} else {
		p.Date = time.Now()
	}

	if froms, err := h.AddressList("From"); err == nil && len(froms) > 0 {
		p.From = toAddress(froms[0])
	} else {
		p.From = parseAddrFallback(h.Get("From"))
	}
	p.To = toAddresses(h, "To")
	p.Cc = toAddresses(h, "Cc")
	p.Bcc = toAddresses(h, "Bcc")

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read part: %w", err)
		}
		switch ph := part.Header.(type) {
		case *gomail.InlineHeader:
			ct, _, _ := ph.ContentType()
			body, err := io.ReadAll(part.Body)
			if err != nil {
				return nil, fmt.Errorf("read inline body: %w", err)
			}
			switch strings.ToLower(ct) {
			case "text/html":
				if p.BodyHTML == "" {
					p.BodyHTML = string(body)
				}
			default:
				if p.BodyText == "" {
					p.BodyText = string(body)
				}
			}
		case *gomail.AttachmentHeader:
			if casStore == nil {
				continue
			}
			filename, _ := ph.Filename()
			ct, _, _ := ph.ContentType()
			sum, rel, size, err := casStore.Put(part.Body)
			if err != nil {
				return nil, fmt.Errorf("store attachment: %w", err)
			}
			p.Attachments = append(p.Attachments, store.Attachment{
				Filename:  filename,
				MimeType:  ct,
				SizeBytes: size,
				SHA256:    sum,
				Path:      rel,
			})
		default:
			_ = ph
		}
	}

	if p.MessageID == "" {
		p.MessageID = syntheticMessageID(raw)
	}
	return p, nil
}

func toAddress(a *mail.Address) store.Address {
	if a == nil {
		return store.Address{}
	}
	return store.Address{Name: a.Name, Email: strings.ToLower(a.Address)}
}

func toAddresses(h gomail.Header, field string) []store.Address {
	list, err := h.AddressList(field)
	if err != nil || len(list) == 0 {
		return nil
	}
	out := make([]store.Address, 0, len(list))
	for _, a := range list {
		out = append(out, toAddress(a))
	}
	return out
}

func parseAddrFallback(raw string) store.Address {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return store.Address{}
	}
	a, err := mail.ParseAddress(raw)
	if err != nil {
		return store.Address{Email: strings.ToLower(raw)}
	}
	return toAddress(a)
}

// syntheticMessageID derives a stable ID for messages that lack a Message-ID header,
// so they can still be deduplicated across runs.
func syntheticMessageID(raw []byte) string {
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("synthetic.%s@mail-indexer.local", hex.EncodeToString(sum[:16]))
}
