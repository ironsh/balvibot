package mcpserver

import (
	"time"

	"github.com/ironcd/philanthropy-os/tools/mail-indexer/internal/store"
)

// All times are RFC 3339 strings on the wire. The server parses them with
// time.RFC3339 / time.RFC3339Nano (both accepted). Empty strings mean "no bound".

type ListGranteesInput struct{}

type ListGranteesOutput struct {
	Grantees []store.Grantee `json:"grantees"`
}

type ListEmailsForGranteeInput struct {
	GranteeID string `json:"grantee_id" jsonschema:"Grantee id from list_grantees. Use '_unassigned' to list messages whose grantee could not be determined."`
	Since     string `json:"since,omitempty" jsonschema:"Optional RFC 3339 lower bound on message date (inclusive)."`
	Until     string `json:"until,omitempty" jsonschema:"Optional RFC 3339 upper bound on message date (inclusive)."`
	Limit     int    `json:"limit,omitempty" jsonschema:"Max results per page (1-200, default 50)."`
	Cursor    int64  `json:"cursor,omitempty" jsonschema:"Pagination cursor from a prior call's next_cursor (omit on first call)."`
}

type ListEmailsForGranteeOutput struct {
	Messages   []store.MessageSummary `json:"messages"`
	NextCursor int64                  `json:"next_cursor,omitempty"`
}

type GetEmailInput struct {
	ID string `json:"id" jsonschema:"Either the numeric internal id (as a string) or the RFC 5322 Message-ID."`
}

type GetEmailOutput struct {
	Message     *MessageDetail     `json:"message"`
	Attachments []store.Attachment `json:"attachments,omitempty"`
}

type MessageDetail struct {
	ID          int64             `json:"id"`
	MessageID   string            `json:"message_id"`
	ThreadID    int64             `json:"thread_id"`
	GranteeID   *string           `json:"grantee_id,omitempty"`
	Folder      string            `json:"folder"`
	InReplyTo   string            `json:"in_reply_to,omitempty"`
	References  []string          `json:"references,omitempty"`
	From        store.Address     `json:"from"`
	To          []store.Address   `json:"to,omitempty"`
	Cc          []store.Address   `json:"cc,omitempty"`
	Recipients  []store.Recipient `json:"recipients,omitempty"`
	Subject     string            `json:"subject,omitempty"`
	Date        time.Time         `json:"date"`
	BodyText    string            `json:"body_text,omitempty"`
	BodyHTML    string            `json:"body_html,omitempty"`
	SizeBytes   int64             `json:"size_bytes"`
}

type ListThreadsForGranteeInput struct {
	GranteeID string `json:"grantee_id" jsonschema:"Grantee id from list_grantees. Use '_unassigned' for threads with no grantee attribution."`
	Since     string `json:"since,omitempty" jsonschema:"Optional RFC 3339 lower bound on last_seen_at."`
	Until     string `json:"until,omitempty" jsonschema:"Optional RFC 3339 upper bound on first_seen_at."`
	Limit     int    `json:"limit,omitempty" jsonschema:"Max results per page (1-200, default 50)."`
	Cursor    int64  `json:"cursor,omitempty"`
}

type ListThreadsForGranteeOutput struct {
	Threads    []store.ThreadSummary `json:"threads"`
	NextCursor int64                 `json:"next_cursor,omitempty"`
}

type GetThreadInput struct {
	ThreadID int64 `json:"thread_id" jsonschema:"Internal thread id (from list_threads_for_grantee)."`
}

type GetThreadOutput struct {
	Thread   *store.Thread          `json:"thread"`
	Messages []store.MessageSummary `json:"messages"`
}

type SearchEmailsInput struct {
	GranteeID string `json:"grantee_id,omitempty" jsonschema:"Optional grantee filter. Use '_unassigned' for messages with no grantee."`
	From      string `json:"from,omitempty" jsonschema:"Case-insensitive substring match against the From: address."`
	Subject   string `json:"subject,omitempty" jsonschema:"Case-insensitive substring match against the Subject: header."`
	Body      string `json:"body,omitempty" jsonschema:"Case-insensitive substring match against the plain-text body."`
	Since     string `json:"since,omitempty" jsonschema:"Optional RFC 3339 lower bound on message date."`
	Until     string `json:"until,omitempty" jsonschema:"Optional RFC 3339 upper bound on message date."`
	Limit     int    `json:"limit,omitempty"`
	Cursor    int64  `json:"cursor,omitempty"`
}

type SearchEmailsOutput struct {
	Messages   []store.MessageSummary `json:"messages"`
	NextCursor int64                  `json:"next_cursor,omitempty"`
}

type ListAttachmentsInput struct {
	MessageID int64 `json:"message_id" jsonschema:"Internal message id (NOT the RFC 5322 Message-ID string)."`
}

type ListAttachmentsOutput struct {
	Attachments []store.Attachment `json:"attachments"`
}
