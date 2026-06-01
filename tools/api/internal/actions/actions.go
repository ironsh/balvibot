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

// Metadata is the JSON stored in approval_actions.metadata. It carries context
// about the request itself (as opposed to the action's payload, which lives in
// args). All fields are optional.
type Metadata struct {
	// SignalNumber is the Signal phone number that requested the action, if it
	// originated from a Signal conversation.
	SignalNumber string `json:"signal_number,omitempty"`
}
