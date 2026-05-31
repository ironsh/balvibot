// Command balvi-approve is the offline operator CLI for the approval service.
// It lists pending actions, shows their details, and approves one by signing
// the action's canonical payload with the operator's SSH key (via ssh-agent or
// a private key file). It talks to `api approve-serve` over HTTP and never
// touches the database directly.
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
	"os"
	"path/filepath"
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
	server string
	email  string
	key    string
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
	root.PersistentFlags().StringVar(&g.key, "key", defaultKeyPath(), "Path to your SSH private key (used directly or to select the ssh-agent key).")
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

			signer, err := getSigner(g.key)
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

// getSigner returns an ssh.Signer for keyPath. It prefers ssh-agent (matching
// keyPath.pub when available), and falls back to reading the private key file.
func getSigner(keyPath string) (ssh.Signer, error) {
	var want ssh.PublicKey
	if pubBytes, err := os.ReadFile(keyPath + ".pub"); err == nil {
		if pub, _, _, _, perr := ssh.ParseAuthorizedKey(pubBytes); perr == nil {
			want = pub
		}
	}

	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			defer conn.Close()
			if signers, err := agent.NewClient(conn).Signers(); err == nil && len(signers) > 0 {
				if want != nil {
					for _, s := range signers {
						if bytes.Equal(s.PublicKey().Marshal(), want.Marshal()) {
							return s, nil
						}
					}
				} else if len(signers) == 1 {
					return signers[0], nil
				}
			}
		}
	}

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
