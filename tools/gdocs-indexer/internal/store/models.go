package store

import "time"

const (
	SourceTypeFolder = "folder"
	SourceTypeDoc    = "doc"

	StatusActive = "active"
	StatusPaused = "paused"
	StatusStale  = "stale"
	StatusError  = "error"

	UnregisteredPending    = "pending"
	UnregisteredIgnored    = "ignored"
	UnregisteredBlocked    = "blocked"
	UnregisteredRegistered = "registered"
)

type Grantee struct {
	GranteeID   string    `json:"grantee_id"`
	OwnerEmail  string    `json:"owner_email"`
	DisplayName string    `json:"display_name,omitempty"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

type GranteeSummary struct {
	GranteeID     string `json:"grantee_id"`
	OwnerEmail    string `json:"owner_email"`
	DisplayName   string `json:"display_name,omitempty"`
	DocumentCount int    `json:"document_count"`
}

type Source struct {
	ID         int64     `json:"id"`
	GranteeID  string    `json:"grantee_id"`
	SourceType string    `json:"source_type"`
	DriveID    string    `json:"drive_id"`
	AddedAt    time.Time `json:"added_at"`
}

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

type UnregisteredDoc struct {
	DocID      string    `json:"doc_id"`
	OwnerEmail string    `json:"owner_email,omitempty"`
	Title      string    `json:"title,omitempty"`
	MimeType   string    `json:"mime_type,omitempty"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
	Status     string    `json:"status"`
}
