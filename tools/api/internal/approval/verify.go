package approval

import (
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// ErrBadSignature is returned when a signature does not verify against the
// authorized public key.
var ErrBadSignature = errors.New("signature verification failed")

// MarshalSignature encodes an ssh.Signature as the base64 wire form the CLI
// sends to the server.
func MarshalSignature(sig *ssh.Signature) string {
	return base64.StdEncoding.EncodeToString(ssh.Marshal(sig))
}

// Verify checks that b64Sig is a valid signature of payload produced by the
// private key matching pubKeyAuthorized (an authorized_keys-format line).
func Verify(pubKeyAuthorized string, payload []byte, b64Sig string) error {
	pub, err := ParseAuthorizedKey(pubKeyAuthorized)
	if err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(b64Sig)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	var sig ssh.Signature
	if err := ssh.Unmarshal(raw, &sig); err != nil {
		return fmt.Errorf("parse signature: %w", err)
	}
	if err := pub.Verify(payload, &sig); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	return nil
}
