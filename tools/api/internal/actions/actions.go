// Package actions defines the approval-gated actions shared by their two
// halves: the MCP tools that enqueue them (producer, in mcpserver) and the
// executor handlers that run them once approved (consumer, registered into the
// approval service). Keeping the action name constants and argument structs in
// one place guarantees the producer and consumer agree on the wire shape stored
// in approval_actions.args.
package actions

// Action name constants. The enqueueing MCP tool and the executor handler key
// off the same string.
const (
	ActionAddGrantee      = "add_grantee"
	ActionAddApprovalUser = "add_approval_user"
	ActionWhitelistDoc    = "whitelist_doc"
)

// AddGranteeArgs is the JSON stored in approval_actions.args for ActionAddGrantee.
type AddGranteeArgs struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name,omitempty"`
}

// AddApprovalUserArgs is the JSON stored in approval_actions.args for
// ActionAddApprovalUser.
type AddApprovalUserArgs struct {
	Email     string `json:"email"`
	PublicKey string `json:"ssh_public_key"`
}

// WhitelistDocArgs is the JSON stored in approval_actions.args for
// ActionWhitelistDoc. SourceType ("folder" or "doc") is resolved from the Drive
// object's MIME type by the enqueueing MCP tool, so the executor (which has no
// Drive access) and the human operator both see the already-classified type.
type WhitelistDocArgs struct {
	GranteeID  string `json:"grantee_id"`
	DriveID    string `json:"drive_id"`
	SourceType string `json:"source_type"`
}

// Metadata is the JSON stored in approval_actions.metadata. It carries context
// about the request itself (as opposed to the action's payload, which lives in
// args). All fields are optional.
type Metadata struct {
	// SignalNumber is the Signal phone number that requested the action, if it
	// originated from a Signal conversation.
	SignalNumber string `json:"signal_number,omitempty"`
}
