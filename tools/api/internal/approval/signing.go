// Package approval holds the approval-service domain logic: the canonical
// signing payload, SSH signature verification, key fingerprinting, and the
// action executor registry. It is shared by the approval server and the
// offline balvi-approve CLI.
package approval

import (
	"bytes"
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
// Both the CLI and the server MUST construct this from the same fields. The args
// are compacted to a canonical, whitespace-free form before hashing so the two
// sides always agree on the bytes even though Postgres reformats JSONB on
// storage (adding spaces after colons/commas) and Go's json encoder compacts it
// again in transit. Without this, the spaced Postgres form the server signs and
// the compacted form the client receives over HTTP would diverge.
func SigningPayload(id int64, action string, args json.RawMessage) []byte {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, args); err != nil {
		// Not valid JSON; fall back to the raw bytes so behavior is still
		// deterministic (both sides will agree as long as the bytes match).
		compact.Reset()
		compact.Write(args)
	}
	return []byte(fmt.Sprintf("%s\n%d\n%s\n%s", PayloadVersion, id, action, compact.Bytes()))
}
