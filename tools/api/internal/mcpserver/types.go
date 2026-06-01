package mcpserver

import (
	"time"

	"github.com/ironsh/balvibot/tools/api/internal/store"
)

// All times are RFC 3339 strings on the wire. Empty strings mean "no bound".

// ---------- actions (write-for-approval) ----------

// EnqueueResult is the common output of every mutating tool: the action was
// queued for approval, not performed. Returned with status "pending".
type EnqueueResult struct {
	ApprovalID int64  `json:"approval_id"`
	Action     string `json:"action"`
	Status     string `json:"status"`
}

// ListApprovalsInput filters the approval queue. An empty status returns every
// action regardless of state.
type ListApprovalsInput struct {
	Status string `json:"status,omitempty" jsonschema:"Optional status filter: pending, executed, failed, or rejected. Omit to return actions in every state."`
}

// ApprovalView is the wire representation of a queued approval action. It
// mirrors store.ApprovalAction but renders the Args and Metadata columns as
// decoded objects rather than store.ApprovalAction's json.RawMessage. A
// json.RawMessage is a []byte, which the SDK reflects into the output schema as
// a (nullable) array; the executor returns the raw JSON object verbatim, so the
// response fails its own schema. Typing these as objects matches the actual
// payload (each action's args/metadata are key/value maps).
type ApprovalView struct {
	ID          int64          `json:"id"`
	Action      string         `json:"action"`
	Args        map[string]any `json:"args"`
	Metadata    map[string]any `json:"metadata"`
	Status      string         `json:"status"`
	RequestedBy string         `json:"requested_by,omitempty"`
	ApprovedBy  string         `json:"approved_by,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	ApprovedAt  *time.Time     `json:"approved_at,omitempty"`
	ExecutedAt  *time.Time     `json:"executed_at,omitempty"`
	LastError   string         `json:"last_error,omitempty"`
}

// ListApprovalsOutput is the approval queue, newest first.
type ListApprovalsOutput struct {
	Approvals []ApprovalView `json:"approvals"`
}

// AddGranteeInput requests creation of a new grantee (queued for approval).
type AddGranteeInput struct {
	Slug         string `json:"slug" jsonschema:"Stable grantee id/slug to create (e.g. acme-foundation). Must be unique."`
	DisplayName  string `json:"display_name,omitempty" jsonschema:"Optional human-readable name."`
	SignalNumber string `json:"signal_number,omitempty" jsonschema:"Optional Signal phone number that requested this action."`
	RequestedBy  string `json:"requested_by,omitempty" jsonschema:"Optional identifier of who/what requested this action."`
}

// AddApprovalUserInput requests authorization of a new approval operator
// (queued for approval).
type AddApprovalUserInput struct {
	Email        string `json:"email" jsonschema:"Operator email address. Must be unique."`
	PublicKey    string `json:"ssh_public_key" jsonschema:"Operator SSH public key in authorized_keys format (e.g. 'ssh-ed25519 AAAA... comment')."`
	SignalNumber string `json:"signal_number,omitempty" jsonschema:"Optional Signal phone number that requested this action."`
	RequestedBy  string `json:"requested_by,omitempty" jsonschema:"Optional identifier of who/what requested this action."`
}

// WhitelistDocInput requests that a Drive folder or doc be ingested for a
// grantee (queued for approval). Folder vs doc is auto-detected from the Drive
// object at approval time, so the caller only supplies the id.
type WhitelistDocInput struct {
	GranteeID    string `json:"grantee_id" jsonschema:"Existing grantee id (from list_grantees) to attach this source to."`
	DriveID      string `json:"drive_id" jsonschema:"Google Drive id of a Google Doc or a folder to ingest. Folders are walked recursively."`
	SignalNumber string `json:"signal_number,omitempty" jsonschema:"Optional Signal phone number that requested this action."`
	RequestedBy  string `json:"requested_by,omitempty" jsonschema:"Optional identifier of who/what requested this action."`
}

// AuthorizeGranteeEmailInput requests that a sender email address be authorized
// for an existing grantee (queued for approval). Once approved, the mail indexer
// tags messages from this sender with the grantee_id.
type AuthorizeGranteeEmailInput struct {
	GranteeID    string `json:"grantee_id" jsonschema:"Existing grantee id (from list_grantees) to authorize the email for."`
	Email        string `json:"email" jsonschema:"Sender email address to associate with this grantee. Matched case-insensitively against the From: address."`
	SignalNumber string `json:"signal_number,omitempty" jsonschema:"Optional Signal phone number that requested this action."`
	RequestedBy  string `json:"requested_by,omitempty" jsonschema:"Optional identifier of who/what requested this action."`
}

// ---------- notes ----------

// CreateNoteInput records a note about a grantee. Unlike the approval-gated
// tools, this writes immediately and returns the stored note.
type CreateNoteInput struct {
	GranteeID    string `json:"grantee_id" jsonschema:"Existing grantee id (from list_grantees) the note is about."`
	Content      string `json:"content" jsonschema:"The note text to remember about the grantee."`
	Kind         string `json:"kind,omitempty" jsonschema:"Note kind: one of note, fact, preference, status, contact. Defaults to note."`
	SupersedesID int64  `json:"supersedes_id,omitempty" jsonschema:"Optional id of an existing note (for the same grantee) that this note replaces. The superseded note is hidden from list_notes by default but kept for history."`
	SignalNumber string `json:"signal_number,omitempty" jsonschema:"Optional Signal phone number that requested this note."`
}

type CreateNoteOutput struct {
	Note *store.Note `json:"note"`
}

// ListNotesInput lists notes for a grantee, newest first. By default superseded
// notes are omitted; set include_superseded to see the full history.
type ListNotesInput struct {
	GranteeID         string `json:"grantee_id" jsonschema:"Grantee id from list_grantees."`
	Kind              string `json:"kind,omitempty" jsonschema:"Optional kind filter: note, fact, preference, status, or contact."`
	Since             string `json:"since,omitempty" jsonschema:"Optional RFC 3339 lower bound on the note's created_at (inclusive)."`
	Until             string `json:"until,omitempty" jsonschema:"Optional RFC 3339 upper bound on the note's created_at (inclusive)."`
	IncludeSuperseded bool   `json:"include_superseded,omitempty" jsonschema:"Include notes that have been superseded by a newer note. Defaults to false."`
	Limit             int    `json:"limit,omitempty" jsonschema:"Max results per page (1-200, default 50)."`
	Cursor            int64  `json:"cursor,omitempty" jsonschema:"Pagination cursor from a prior call's next_cursor (omit on first call)."`
}

type ListNotesOutput struct {
	Notes      []store.Note `json:"notes"`
	NextCursor int64        `json:"next_cursor,omitempty"`
}

// ---------- grantees ----------

type ListGranteesInput struct{}

type ListGranteesOutput struct {
	Grantees []store.GranteeSummary `json:"grantees"`
}

// ---------- mail ----------

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
	ID         int64             `json:"id"`
	MessageID  string            `json:"message_id"`
	ThreadID   int64             `json:"thread_id"`
	GranteeID  *string           `json:"grantee_id,omitempty"`
	Folder     string            `json:"folder"`
	InReplyTo  string            `json:"in_reply_to,omitempty"`
	References []string          `json:"references,omitempty"`
	From       store.Address     `json:"from"`
	To         []store.Address   `json:"to,omitempty"`
	Cc         []store.Address   `json:"cc,omitempty"`
	Recipients []store.Recipient `json:"recipients,omitempty"`
	Subject    string            `json:"subject,omitempty"`
	Date       time.Time         `json:"date"`
	BodyText   string            `json:"body_text,omitempty"`
	BodyHTML   string            `json:"body_html,omitempty"`
	SizeBytes  int64             `json:"size_bytes"`
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

// ---------- docs ----------

type ListDocumentsForGranteeInput struct {
	GranteeID string `json:"grantee_id" jsonschema:"Grantee id from list_grantees."`
	Limit     int    `json:"limit,omitempty" jsonschema:"Max results per page (1-500, default 50)."`
	Cursor    int64  `json:"cursor,omitempty" jsonschema:"Pagination cursor from a prior call's next_cursor (omit on first call)."`
}

type ListDocumentsForGranteeOutput struct {
	Documents  []store.DocSummary `json:"documents"`
	NextCursor int64              `json:"next_cursor,omitempty"`
}

type GetDocumentInput struct {
	GranteeID string `json:"grantee_id" jsonschema:"Grantee id this document belongs to."`
	DocID     string `json:"doc_id" jsonschema:"Drive file id from list_documents_for_grantee."`
}

type DocumentDetail struct {
	DocID           string    `json:"doc_id"`
	GranteeID       string    `json:"grantee_id"`
	Title           string    `json:"title"`
	ContentMarkdown string    `json:"content_markdown"`
	ModifiedAt      time.Time `json:"modified_at"`
	SyncedAt        time.Time `json:"synced_at"`
	HadImages       bool      `json:"had_images"`
	HadComments     bool      `json:"had_comments"`
	Status          string    `json:"status"`
	Stale           bool      `json:"stale,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
}

type GetDocumentOutput struct {
	Document *DocumentDetail `json:"document"`
}

type DocumentMetadata struct {
	DocID       string    `json:"doc_id"`
	GranteeID   string    `json:"grantee_id"`
	Title       string    `json:"title"`
	ModifiedAt  time.Time `json:"modified_at"`
	SyncedAt    time.Time `json:"synced_at"`
	HadImages   bool      `json:"had_images"`
	HadComments bool      `json:"had_comments"`
	Status      string    `json:"status"`
	Stale       bool      `json:"stale,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

type GetDocumentMetadataOutput struct {
	Document *DocumentMetadata `json:"document"`
}
