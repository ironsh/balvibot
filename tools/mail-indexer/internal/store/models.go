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
