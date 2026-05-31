// Package approval holds the approval-service domain logic: the canonical
// signing payload, SSH signature verification, key fingerprinting, and the
// action executor registry. It is shared by the approval server and the
// offline balvi-approve CLI.
package approval

import (
	"encoding/json"
	"fmt"
)

// PayloadVersion prefixes every signing payload so the format can evolve.
const PayloadVersion = "balvi-approval:v1"

// SigningPayload builds the canonical byte string that the operator signs and
// the server verifies. It binds the approval id, the action name, and the exact
// (server-normalized) args JSON together, so a signature authorizes one
// specific action with specific arguments and cannot be replayed against a
// different row.
//
// Both the CLI and the server MUST construct this from the same fields. The
// server returns the normalized args alongside the payload, so the two sides
// always agree on the bytes even though Postgres reformats JSONB on storage.
func SigningPayload(id int64, action string, args json.RawMessage) []byte {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	return []byte(fmt.Sprintf("%s\n%d\n%s\n%s", PayloadVersion, id, action, args))
}
