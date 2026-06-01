// Command balvi-approve is the offline operator CLI for the approval service.
// It lists pending actions, shows their details, and approves one by signing
// the action's canonical payload with the operator's SSH key (via ssh-agent or
// a private key file). It talks to `api approve-serve` over HTTP and never
// touches the database directly.
//
// Signing key selection: on approve it asks the server for the fingerprint of
// the authorized key registered to --email, then auto-selects that key from
// ssh-agent (or the --key file). This means the 1Password agent's many keys
// just work with no extra flags. --agent-key (SHA256 fingerprint or comment
// substring) is an override for when the server can't be consulted; absent
// both, it prefers ssh-agent (matching --key.pub or its sole key) and falls
// back to the --key private key file.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/ironsh/balvibot/tools/api/internal/approval"
	"github.com/ironsh/balvibot/tools/api/internal/store"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type globals struct {
	server   string
	email    string
	key      string
	agentKey string
}

func newRootCmd() *cobra.Command {
	g := &globals{}
	root := &cobra.Command{
		Use:           "balvi-approve",
		Short:         "Offline operator CLI to review and approve queued actions.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&g.server, "server", envDefault("BALVI_APPROVE_SERVER", "http://localhost:8090"), "Approval service base URL.")
	root.PersistentFlags().StringVar(&g.email, "email", os.Getenv("BALVI_APPROVE_EMAIL"), "Your email (must match an authorized approval user).")
	root.PersistentFlags().StringVar(&g.key, "key", defaultKeyPath(), "Path to your SSH private key (used directly or to select the ssh-agent key via its .pub).")
	root.PersistentFlags().StringVar(&g.agentKey, "agent-key", os.Getenv("BALVI_APPROVE_AGENT_KEY"), "Override key selection: pick an ssh-agent key by SHA256 fingerprint or comment substring (agent-only; skips the auto-select and key file).")
	root.AddCommand(newListCmd(g), newShowCmd(g), newApproveCmd(g))
	return root
}

func newListCmd(g *globals) *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List actions awaiting approval.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var resp struct {
				Actions []store.ApprovalAction `json:"actions"`
			}
			if err := getJSON(cmd.Context(), g.server+"/actions?status="+status, &resp); err != nil {
				return err
			}
			if len(resp.Actions) == 0 {
				fmt.Printf("no actions with status %q\n", status)
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tACTION\tSTATUS\tREQUESTED_BY\tCREATED\tARGS")
			for _, a := range resp.Actions {
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n",
					a.ID, a.Action, a.Status, dashIfEmpty(a.RequestedBy),
					a.CreatedAt.Format(time.RFC3339), truncate(string(a.Args), 60))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&status, "status", store.ApprovalPending, "Filter by status.")
	return cmd
}

func newShowCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show a single action in full.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var view actionView
			if err := getJSON(cmd.Context(), g.server+"/actions/"+args[0], &view); err != nil {
				return err
			}
			printAction(view)
			return nil
		},
	}
}

func newApproveCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "approve <id>",
		Short: "Approve an action: sign it with your SSH key and submit.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if g.email == "" {
				return errors.New("--email is required (must match an authorized approval user)")
			}
			var view actionView
			if err := getJSON(cmd.Context(), g.server+"/actions/"+args[0], &view); err != nil {
				return err
			}
			if view.Status != store.ApprovalPending {
				return fmt.Errorf("action %d is not pending (status=%s)", view.ID, view.Status)
			}

			// Reconstruct the payload locally and verify it matches what the
			// server says we should sign, so we never blindly sign server bytes.
			local := approval.SigningPayload(view.ID, view.Action, view.Args)
			serverPayload, err := base64.StdEncoding.DecodeString(view.SigningPayloadB64)
			if err != nil {
				return fmt.Errorf("decode server payload: %w", err)
			}
			if !bytes.Equal(local, serverPayload) {
				return errors.New("signing payload mismatch between client and server; refusing to sign")
			}

			printAction(view)
			if !yes && !confirm(fmt.Sprintf("Approve and execute action %d (%s)?", view.ID, view.Action)) {
				return errors.New("aborted")
			}

			// Ask the server which key fingerprint this operator must sign with,
			// so we can auto-select the matching ssh-agent key. Best-effort: an
			// older server or unregistered email leaves wantFP empty and we fall
			// back to heuristics.
			var wantFP string
			var who struct {
				Fingerprint string `json:"fingerprint"`
			}
			if err := getJSON(cmd.Context(), g.server+"/approval-users/"+url.PathEscape(g.email), &who); err == nil {
				wantFP = who.Fingerprint
			}

			signer, err := getSigner(g.key, g.agentKey, wantFP)
			if err != nil {
				return err
			}
			sig, err := signer.Sign(rand.Reader, local)
			if err != nil {
				return fmt.Errorf("sign: %w", err)
			}

			body, _ := json.Marshal(map[string]string{
				"email":     g.email,
				"signature": approval.MarshalSignature(sig),
			})
			var resp struct {
				ApprovalID int64  `json:"approval_id"`
				Status     string `json:"status"`
			}
			if err := postJSON(cmd.Context(), g.server+"/actions/"+args[0]+"/approve", body, &resp); err != nil {
				return err
			}
			fmt.Printf("ok: action %d %s\n", resp.ApprovalID, resp.Status)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the confirmation prompt.")
	return cmd
}

// actionView mirrors approvalserver.ActionView (the embedded action plus the
// base64 signing payload).
type actionView struct {
	store.ApprovalAction
	SigningPayloadB64 string `json:"signing_payload_b64"`
}

func printAction(v actionView) {
	fmt.Printf("ID:           %d\n", v.ID)
	fmt.Printf("Action:       %s\n", v.Action)
	fmt.Printf("Status:       %s\n", v.Status)
	fmt.Printf("Requested by: %s\n", dashIfEmpty(v.RequestedBy))
	fmt.Printf("Created:      %s\n", v.CreatedAt.Format(time.RFC3339))
	fmt.Printf("Args:         %s\n", indentJSON(v.Args))
	fmt.Printf("Metadata:     %s\n", indentJSON(v.Metadata))
}

// ---------- ssh signer ----------

// getSigner returns an ssh.Signer to sign approvals with.
//
// Selection precedence:
//   - agentSel (--agent-key): an explicit override; select that agent key by
//     SHA256 fingerprint or comment substring and never touch the key file.
//   - wantFP (the operator's authorized fingerprint, from the server): pick
//     exactly that key, from ssh-agent or the keyPath file. Anything else would
//     fail server-side verification, so a miss is an error, not a fallback.
//   - otherwise: prefer ssh-agent (matching keyPath.pub, or the agent's sole
//     key) and fall back to reading the keyPath private key file.
func getSigner(keyPath, agentSel, wantFP string) (ssh.Signer, error) {
	// Best-effort: the agent may be absent. The connection is intentionally
	// left open for the life of the process, because the returned agent signer
	// signs over it; the OS reaps the fd on exit.
	signers, agentErr := agentSigners()

	// An explicit selector means "use the agent", so surface why it can't.
	if agentSel != "" {
		if len(signers) == 0 {
			return nil, fmt.Errorf("--agent-key %q given but ssh-agent has no usable keys (is SSH_AUTH_SOCK set?): %w", agentSel, agentErr)
		}
		return selectAgentKey(signers, agentSel)
	}

	// The server told us exactly which key this operator must sign with.
	if wantFP != "" {
		if s := agentKeyByFingerprint(signers, wantFP); s != nil {
			return s, nil
		}
		if pubFileFingerprint(keyPath) == wantFP {
			return loadKeyFile(keyPath)
		}
		msg := fmt.Sprintf("your authorized key for this email (%s) is not in ssh-agent or at %s", wantFP, keyPath)
		if len(signers) > 0 {
			msg += "\nssh-agent holds:\n" + formatAgentKeys(signers)
		}
		return nil, errors.New(msg)
	}

	// No fingerprint hint: match an agent key against keyPath.pub if present.
	if fp := pubFileFingerprint(keyPath); fp != "" {
		if s := agentKeyByFingerprint(signers, fp); s != nil {
			return s, nil
		}
	}

	// A single agent key is unambiguous.
	if len(signers) == 1 {
		return signers[0], nil
	}

	signer, err := loadKeyFile(keyPath)
	if err == nil {
		return signer, nil
	}
	if len(signers) > 1 {
		return nil, fmt.Errorf("ssh-agent holds %d keys and none could be auto-selected (no %s.pub to match); pass --agent-key with a fingerprint or comment:\n%s",
			len(signers), keyPath, formatAgentKeys(signers))
	}
	return nil, err
}

// agentKeyByFingerprint returns the agent signer whose public key has the given
// SHA256 fingerprint, or nil.
func agentKeyByFingerprint(signers []ssh.Signer, fp string) ssh.Signer {
	for _, s := range signers {
		if ssh.FingerprintSHA256(s.PublicKey()) == fp {
			return s
		}
	}
	return nil
}

// pubFileFingerprint returns the SHA256 fingerprint of keyPath.pub, or "" if it
// is absent or unparseable.
func pubFileFingerprint(keyPath string) string {
	b, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		return ""
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(b)
	if err != nil {
		return ""
	}
	return ssh.FingerprintSHA256(pub)
}

// loadKeyFile reads and parses the private key at keyPath, turning a
// passphrase-protected key into actionable advice.
func loadKeyFile(keyPath string) (ssh.Signer, error) {
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read key %s: %w", keyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		var pmErr *ssh.PassphraseMissingError
		if errors.As(err, &pmErr) {
			return nil, fmt.Errorf("key %s is passphrase-protected; add it to ssh-agent (ssh-add %s) and retry", keyPath, keyPath)
		}
		return nil, fmt.Errorf("parse key %s: %w", keyPath, err)
	}
	return signer, nil
}

// agentSigners returns the signers held by the ssh-agent at SSH_AUTH_SOCK, or
// nil when no agent is configured.
func agentSigners() ([]ssh.Signer, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("SSH_AUTH_SOCK is not set")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("dial ssh-agent: %w", err)
	}
	return agent.NewClient(conn).Signers()
}

// selectAgentKey picks the single agent signer matching sel, which is either a
// full SHA256 fingerprint (e.g. "SHA256:abc...") or a substring of the key's
// comment. It errors if nothing matches or the match is ambiguous.
func selectAgentKey(signers []ssh.Signer, sel string) (ssh.Signer, error) {
	var matches []ssh.Signer
	for _, s := range signers {
		if ssh.FingerprintSHA256(s.PublicKey()) == sel {
			return s, nil // exact fingerprint: unambiguous by construction
		}
		if c := agentKeyComment(s); c != "" && strings.Contains(c, sel) {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, fmt.Errorf("no ssh-agent key matches %q; available keys:\n%s", sel, formatAgentKeys(signers))
	default:
		return nil, fmt.Errorf("%q matches %d ssh-agent keys; use a full SHA256 fingerprint:\n%s", sel, len(matches), formatAgentKeys(matches))
	}
}

// agentKeyComment returns the comment of an ssh-agent key, if available. The
// agent client exposes it via *agent.Key; other public keys carry no comment.
func agentKeyComment(s ssh.Signer) string {
	if k, ok := s.PublicKey().(*agent.Key); ok {
		return k.Comment
	}
	return ""
}

// formatAgentKeys renders agent keys as indented "fingerprint  comment" lines
// for error messages that ask the operator to pick one.
func formatAgentKeys(signers []ssh.Signer) string {
	var b strings.Builder
	for _, s := range signers {
		fmt.Fprintf(&b, "  %s  %s\n", ssh.FingerprintSHA256(s.PublicKey()), agentKeyComment(s))
	}
	return strings.TrimRight(b.String(), "\n")
}

// ---------- http helpers ----------

func getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	return doJSON(req, out)
}

func postJSON(ctx context.Context, url string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return doJSON(req, out)
}

func doJSON(req *http.Request, out any) error {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s: %s", resp.Status, e.Error)
		}
		return fmt.Errorf("%s: %s", resp.Status, string(data))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// ---------- misc ----------

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	var resp string
	_, _ = fmt.Scanln(&resp)
	return resp == "y" || resp == "Y" || resp == "yes"
}

func indentJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "              ", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func defaultKeyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "id_ed25519"
	}
	return filepath.Join(home, ".ssh", "id_ed25519")
}
