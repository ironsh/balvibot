package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// newAgentWithKeys returns the signers of an in-memory keyring holding n freshly
// generated ed25519 keys.
func newAgentWithKeys(t *testing.T, n int) []ssh.Signer {
	t.Helper()
	kr := agent.NewKeyring()
	for i := 0; i < n; i++ {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, kr.Add(agent.AddedKey{PrivateKey: priv}))
	}
	signers, err := kr.Signers()
	require.NoError(t, err)
	require.Len(t, signers, n)
	return signers
}

func TestSelectAgentKey_ByFingerprint(t *testing.T) {
	signers := newAgentWithKeys(t, 3)
	want := signers[1]
	fp := ssh.FingerprintSHA256(want.PublicKey())

	got, err := selectAgentKey(signers, fp)
	require.NoError(t, err)
	require.Equal(t, want.PublicKey().Marshal(), got.PublicKey().Marshal())
}

func TestSelectAgentKey_NoMatch(t *testing.T) {
	signers := newAgentWithKeys(t, 2)
	_, err := selectAgentKey(signers, "SHA256:does-not-exist")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no ssh-agent key matches")
}

func TestAgentKeyByFingerprint(t *testing.T) {
	signers := newAgentWithKeys(t, 4)
	want := signers[2]
	fp := ssh.FingerprintSHA256(want.PublicKey())

	got := agentKeyByFingerprint(signers, fp)
	require.NotNil(t, got)
	require.Equal(t, want.PublicKey().Marshal(), got.PublicKey().Marshal())

	require.Nil(t, agentKeyByFingerprint(signers, "SHA256:nope"))
}
