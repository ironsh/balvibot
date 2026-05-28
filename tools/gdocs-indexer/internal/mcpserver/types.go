package mcpserver

import (
	"time"

	"github.com/ironcd/philanthropy-os/tools/gdocs-indexer/internal/store"
)

type ListGranteesInput struct{}

type ListGranteesOutput struct {
	Grantees []store.GranteeSummary `json:"grantees"`
}

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
