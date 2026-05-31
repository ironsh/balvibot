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
	ActionAddGrantee = "add_grantee"
)

// AddGranteeArgs is the JSON stored in approval_actions.args for ActionAddGrantee.
type AddGranteeArgs struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name,omitempty"`
}
