package approval

import (
	"fmt"

	"golang.org/x/crypto/ssh"
)

// ParseAuthorizedKey parses a single authorized_keys-format public key line
// (e.g. "ssh-ed25519 AAAA... comment") into an ssh.PublicKey.
func ParseAuthorizedKey(line string) (ssh.PublicKey, error) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	return pub, nil
}

// Fingerprint returns the SHA256:... fingerprint for an authorized_keys line.
func Fingerprint(line string) (string, error) {
	pub, err := ParseAuthorizedKey(line)
	if err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(pub), nil
}
