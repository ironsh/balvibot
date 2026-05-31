package approval_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ironsh/balvibot/tools/api/internal/approval"
)

// newKey returns an ssh.Signer and its authorized_keys line.
func newKey(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	return signer, line
}

func TestSignVerifyRoundTrip(t *testing.T) {
	signer, pubLine := newKey(t)
	payload := approval.SigningPayload(42, "send_email", json.RawMessage(`{"to":"a@b.com"}`))

	sig, err := signer.Sign(rand.Reader, payload)
	require.NoError(t, err)
	b64 := approval.MarshalSignature(sig)

	require.NoError(t, approval.Verify(pubLine, payload, b64))
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	signer, pubLine := newKey(t)
	payload := approval.SigningPayload(42, "send_email", json.RawMessage(`{"to":"a@b.com"}`))
	sig, err := signer.Sign(rand.Reader, payload)
	require.NoError(t, err)
	b64 := approval.MarshalSignature(sig)

	// Different id => different payload => verification must fail.
	other := approval.SigningPayload(43, "send_email", json.RawMessage(`{"to":"a@b.com"}`))
	require.ErrorIs(t, approval.Verify(pubLine, other, b64), approval.ErrBadSignature)
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	signer, _ := newKey(t)
	_, otherLine := newKey(t)
	payload := approval.SigningPayload(1, "noop", nil)
	sig, err := signer.Sign(rand.Reader, payload)
	require.NoError(t, err)

	require.ErrorIs(t, approval.Verify(otherLine, payload, approval.MarshalSignature(sig)), approval.ErrBadSignature)
}

func TestFingerprint(t *testing.T) {
	_, pubLine := newKey(t)
	fp, err := approval.Fingerprint(pubLine)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(fp, "SHA256:"))
}

func TestDispatch(t *testing.T) {
	reg := approval.NewRegistry()
	var gotArgs string
	reg.Register("noop", func(_ context.Context, args json.RawMessage) error {
		gotArgs = string(args)
		return nil
	})

	require.NoError(t, reg.Dispatch(context.Background(), "noop", json.RawMessage(`{"x":1}`)))
	require.Equal(t, `{"x":1}`, gotArgs)
	require.True(t, reg.Has("noop"))

	err := reg.Dispatch(context.Background(), "missing", nil)
	require.ErrorIs(t, err, approval.ErrUnknownAction)
}
