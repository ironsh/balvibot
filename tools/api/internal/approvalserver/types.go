package approvalserver

import (
	"encoding/json"

	"github.com/ironsh/balvibot/tools/api/internal/store"
)

// ---------- REST (balvi-approve CLI) ----------

// ActionView is the JSON shape returned by the list/get endpoints.
type ActionView struct {
	store.ApprovalAction
	// SigningPayloadB64 is the base64 of the exact bytes the operator must sign
	// for this action. Only populated by the single-action GET endpoint.
	SigningPayloadB64 string `json:"signing_payload_b64,omitempty"`
}

// ListActionsResponse is the GET /actions body.
type ListActionsResponse struct {
	Actions []store.ApprovalAction `json:"actions"`
}

// ApprovalUserView is the GET /approval-users/{email} body. It lets the CLI
// learn which key fingerprint an operator must sign with, so it can
// auto-select the matching ssh-agent key.
type ApprovalUserView struct {
	Email       string `json:"email"`
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"ssh_public_key"`
}

// ApproveRequest is the POST /actions/{id}/approve body. Signature is the
// base64-encoded ssh.Signature of the action's signing payload.
type ApproveRequest struct {
	Email     string `json:"email"`
	Signature string `json:"signature"`
}

// ApproveResponse is returned after a successful approval+dispatch.
type ApproveResponse struct {
	ApprovalID int64  `json:"approval_id"`
	Status     string `json:"status"`
}

// errorResponse is the JSON body for error replies.
type errorResponse struct {
	Error string `json:"error"`
}

func rawOrEmpty(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return b
}
