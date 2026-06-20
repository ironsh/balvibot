# macOS LaunchAgent For llama-server

This directory installs a per-user LaunchAgent that starts `llama-server` on
login with the Qwen GGUF model used by balvibot.

Before installing the LaunchAgent, install the model once so launchd does not
perform the first large download in the background:

```sh
llama-server -hf unsloth/Qwen3.5-9B-GGUF:Q4_K_M
```

Wait for the model download to complete, then stop the server with `Ctrl-C`.

Install and start the LaunchAgent:

```sh
./macos-launch-agent/install.sh
```

The service listens on all interfaces through `--host 0.0.0.0`.

## Remote k3s access over Tailscale

For a remote k3s cluster, expose the Mac's `llama-server` endpoint through the
Tailscale Kubernetes operator with an egress `ProxyGroup`. The Mac must be on
the same Tailnet and its Tailnet DNS name should replace
`your-mac-hostname.tailnet-name.ts.net` below.

```yaml
apiVersion: tailscale.com/v1alpha1
kind: Tailnet
metadata:
  name: balvi
spec:
  credentials:
    secretName: tailnet-balvi-oauth
---
apiVersion: tailscale.com/v1alpha1
kind: ProxyGroup
metadata:
  name: egress-balvibot
spec:
  type: egress
  replicas: 1
  tailnet: balvi
---
apiVersion: v1
kind: Service
metadata:
  name: balvibot-llama
  namespace: balvibot
  annotations:
    tailscale.com/tailnet-fqdn: your-mac-hostname.tailnet-name.ts.net
    tailscale.com/proxy-group: egress-balvibot
spec:
  type: ExternalName
  externalName: placeholder
  ports:
    - name: http
      port: 8080
      protocol: TCP
```

After applying the resources, workloads in the `balvibot` namespace can reach
the Mac-hosted server at `http://balvibot-llama.balvibot.svc.cluster.local:8080`.
The Tailscale operator overwrites the placeholder `externalName`.

Useful commands:

```sh
launchctl print gui/$(id -u)/com.balvi.balvibot.llama-server
launchctl kickstart -k gui/$(id -u)/com.balvi.balvibot.llama-server
tail -f "$HOME/Library/Logs/balvibot-llama-server.out.log"
tail -f "$HOME/Library/Logs/balvibot-llama-server.err.log"
```

Uninstall:

```sh
./macos-launch-agent/uninstall.sh
```
