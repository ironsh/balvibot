package store

import "time"

type Address struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

type Message struct {
	ID            int64
	MessageID     string
	ThreadID      int64
	GranteeID     *string
	Folder        string
	UID           uint32
	UIDValidity   uint32
	InReplyTo     string
	References    []string
	From          Address
	To            []Address
	Cc            []Address
	Bcc           []Address
	Subject       string
	Date          time.Time
	BodyText      string
	BodyHTML      string
	SizeBytes     int64
	Attachments   []Attachment
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
	Folder       string
	UIDValidity  uint32
	LastUID      uint32
}

type Grantee struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Emails    []string  `json:"emails"`
	CreatedAt time.Time `json:"created_at"`
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

// Sentinel grantee id used by the query API to select messages/threads
// whose grantee_id is NULL.
const UnassignedGrantee = "_unassigned"

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
