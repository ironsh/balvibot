package store

import "time"

// ---------- grantees (unified) ----------

const (
	StatusActive = "active"
	StatusPaused = "paused"
	StatusStale  = "stale"
	StatusError  = "error"

	SourceTypeFolder = "folder"
	SourceTypeDoc    = "doc"

	// Note kinds. The default is NoteKindNote; the others let the agent tag a
	// note with what it captures so reads can be filtered.
	NoteKindNote       = "note"
	NoteKindFact       = "fact"
	NoteKindPreference = "preference"
	NoteKindStatus     = "status"
	NoteKindContact    = "contact"

	// UnassignedGrantee is the sentinel id used by the mail query API to
	// select messages/threads whose grantee_id is NULL.
	UnassignedGrantee = "_unassigned"
)

// NoteKinds is the set of valid note kinds, in declaration order. Used to
// validate input and to document the enum.
var NoteKinds = []string{NoteKindNote, NoteKindFact, NoteKindPreference, NoteKindStatus, NoteKindContact}

// ValidNoteKind reports whether kind is one of the allowed note kinds.
func ValidNoteKind(kind string) bool {
	for _, k := range NoteKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// Grantee is the single, unified grantee record shared by mail + docs. It
// merges the gdocs model (grantee_id/display_name/status) with the mail
// side's email mapping.
type Grantee struct {
	GranteeID   string    `json:"grantee_id"`
	DisplayName string    `json:"display_name,omitempty"`
	Status      string    `json:"status"`
	Emails      []string  `json:"emails"`
	CreatedAt   time.Time `json:"created_at"`
}

type GranteeSummary struct {
	GranteeID     string   `json:"grantee_id"`
	DisplayName   string   `json:"display_name,omitempty"`
	Status        string   `json:"status"`
	Emails        []string `json:"emails"`
	DocumentCount int      `json:"document_count"`
}

type Source struct {
	ID         int64     `json:"id"`
	GranteeID  string    `json:"grantee_id"`
	SourceType string    `json:"source_type"`
	DriveID    string    `json:"drive_id"`
	AddedAt    time.Time `json:"added_at"`
}

// ---------- mail ----------

type Address struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

type Message struct {
	ID          int64
	MessageID   string
	ThreadID    int64
	GranteeID   *string
	Folder      string
	UID         uint32
	UIDValidity uint32
	InReplyTo   string
	References  []string
	From        Address
	To          []Address
	Cc          []Address
	Bcc         []Address
	Subject     string
	Date        time.Time
	BodyText    string
	BodyHTML    string
	SizeBytes   int64
	Attachments []Attachment
}

type Thread struct {
	ID            int64
	RootMessageID string
	SubjectNorm   string
	GranteeID     *string
	FirstSeenAt   time.Time
	LastSeenAt    time.Time
}

type Attachment struct {
	ID        int64
	Filename  string
	MimeType  string
	SizeBytes int64
	SHA256    string
	Path      string
}

type MailboxState struct {
	Folder      string
	UIDValidity uint32
	LastUID     uint32
}

type MessageSummary struct {
	ID             int64     `json:"id"`
	MessageID      string    `json:"message_id"`
	ThreadID       int64     `json:"thread_id"`
	GranteeID      *string   `json:"grantee_id,omitempty"`
	Folder         string    `json:"folder"`
	From           Address   `json:"from"`
	Subject        string    `json:"subject,omitempty"`
	Date           time.Time `json:"date"`
	HasAttachments bool      `json:"has_attachments"`
	Snippet        string    `json:"snippet,omitempty"`
}

type ThreadSummary struct {
	ID            int64     `json:"id"`
	RootMessageID string    `json:"root_message_id"`
	SubjectNorm   string    `json:"subject_norm,omitempty"`
	GranteeID     *string   `json:"grantee_id,omitempty"`
	FirstSeenAt   time.Time `json:"first_seen_at"`
	LastSeenAt    time.Time `json:"last_seen_at"`
	MessageCount  int       `json:"message_count"`
}

type Recipient struct {
	Kind  string `json:"kind"`
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type SearchParams struct {
	GranteeID string
	From      string
	Subject   string
	Body      string
	Since     *time.Time
	Until     *time.Time
	Limit     int
	Cursor    int64
}

// ---------- docs ----------

type Doc struct {
	DocID           string    `json:"doc_id"`
	GranteeID       string    `json:"grantee_id"`
	Title           string    `json:"title"`
	OwnerEmail      string    `json:"owner_email"`
	ContentMarkdown string    `json:"content_markdown"`
	ModifiedAt      time.Time `json:"modified_at"`
	SyncedAt        time.Time `json:"synced_at"`
	SourceType      string    `json:"source_type"`
	SourceDriveID   string    `json:"source_drive_id"`
	HadImages       bool      `json:"had_images"`
	HadComments     bool      `json:"had_comments"`
	Status          string    `json:"status"`
	LastError       string    `json:"last_error,omitempty"`
}

type DocSummary struct {
	DocID       string    `json:"doc_id"`
	Title       string    `json:"title"`
	ModifiedAt  time.Time `json:"modified_at"`
	SyncedAt    time.Time `json:"synced_at"`
	HadImages   bool      `json:"had_images"`
	HadComments bool      `json:"had_comments"`
	Status      string    `json:"status"`
}

// ---------- notes ----------

// Note is a free-text note the agent keeps about a grantee. SignalNumber is the
// Signal phone number that requested it (if any). SupersedesID, when set, points
// at the note this one replaces. SupersededByID, when set, is the id of a newer
// note that supersedes this one (computed at read time, not stored).
type Note struct {
	ID             int64     `json:"id"`
	GranteeID      string    `json:"grantee_id"`
	Kind           string    `json:"kind"`
	Content        string    `json:"content"`
	SignalNumber   string    `json:"signal_number,omitempty"`
	SupersedesID   *int64    `json:"supersedes_id,omitempty"`
	SupersededByID *int64    `json:"superseded_by_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// NoteFilter narrows a ListNotes query. GranteeID is required; the rest are
// optional. IncludeSuperseded defaults to false (superseded notes are hidden).
type NoteFilter struct {
	GranteeID         string
	Kind              string
	Since             *time.Time
	Until             *time.Time
	IncludeSuperseded bool
	Limit             int
	Cursor            int64
}
