# balvibot (Helm)

Kubernetes manifests for the balvibot services, packaged as a Helm chart.

## Services

- **protonmail-bridge** — locally built image (see `docker/protonmail-bridge/`,
  ported from [`shenxn/protonmail-bridge-docker`](https://github.com/shenxn/protonmail-bridge-docker))
  exposing local SMTP (25) and IMAP (143) endpoints that proxy a ProtonMail
  account so cluster workloads can send/receive mail.
- **postgres** — in-cluster Postgres StatefulSet, the single source of truth
  for grantees, indexed mail, and mirrored docs. Schema is managed by Goose
  migrations (run once by the `api-migrate` Job on each install/upgrade).
- **api** — the consolidated backend (`tools/api`, one image run as several
  Deployments via subcommands): `serve` exposes the MCP endpoint (grantees +
  mail + docs tools) the agent talks to; `index-mail` indexes mail from the
  bridge into Postgres, tagging messages by grantee; `sync-gdocs` mirrors
  grantee Google Docs from Drive into Postgres. Grantees, their sender emails,
  and their authorized Drive sources are managed with the `api grantee` CLI.
- **hermes-agent** — runs [`nousresearch/hermes-agent`](https://hermes-agent.nousresearch.com/docs/user-guide/docker)
  in gateway mode, exposing an OpenAI-compatible API (8642) and dashboard (9119).
- **signal-cli** — upstream [`AsamK/signal-cli`](https://github.com/AsamK/signal-cli)
  GHCR image running the JSON-RPC HTTP daemon (8080) so cluster workloads can
  send and receive Signal messages.
- **hermes-skills** — locally built busybox-based bundle of chart-built-in
  hermes skills (see `docker/hermes-skills/skills/`). Pulled by an init
  container in the hermes-agent pod and synced into
  `/opt/data/skills/balvibot/` on the Hermes PVC.
- **iron-proxy** — [`ironsh/iron-proxy`](https://docs.iron.sh) egress firewall,
  run as a dedicated pod (Deployment + Service with a pinned ClusterIP).
  Hermes-agent's `dnsConfig` is overridden to use iron-proxy as its sole
  nameserver, and a NetworkPolicy on the hermes pod denies every egress
  destination except iron-proxy and the in-namespace MCP services. iron-proxy
  MITM-decrypts the TLS using a bootstrapped CA, enforces a default-deny
  domain allowlist, and replaces static placeholder API tokens with the real
  LLM-provider keys — so the hermes container never sees the real
  credentials and cannot reach the internet by any path other than the proxy.

## Layout

```
helm/balvibot/Chart.yaml      Chart metadata
helm/balvibot/values.yaml     Tunables (image tags, env, resources)
helm/balvibot/templates/      One file per service
```

Each service template renders its Deployment, Service (where applicable), and
PVC. Add a new service by dropping a new template file alongside the existing
ones and a matching block in `values.yaml`.

## Deploy

Build the local images first (they're referenced by tag, not pulled):

```sh
just build-protonmail-bridge
just build-api
just build-hermes-skills
```

Install/upgrade the chart:

```sh
just deploy
```

This runs `helm upgrade --install balvibot helm/balvibot
--namespace balvibot --create-namespace`. On each install/upgrade the
`api-migrate` Job runs `api migrate up` against Postgres to apply schema
migrations before the services serve traffic.

Grantees are not configured via a file. After the chart is up, manage them with
the `api grantee` CLI (see below).

If your cluster is remote, load the locally built images into it (the justfile
does this for you over SSH via `just upload-protonmail-bridge` / `just
upload-api`, or use `kind load docker-image …`, `minikube image load …`, etc.).

## Secrets

Secrets are provisioned out-of-band from a `.env` file (gitignored, auto-loaded
by the justfile). Bootstrap once from `.env.example`:

```sh
cp .env.example .env
# edit .env, fill in BALVIBOT_* values

just bootstrap-secrets
just bootstrap-iron-proxy-ca
```

`bootstrap-secrets` is idempotent — re-run any time to roll a value. It writes:
`hermes-agent-secrets` (API_SERVER_KEY only); `iron-proxy-secrets`
(REAL_ANTHROPIC_API_KEY / REAL_OPENAI_API_KEY — the actual provider keys,
visible only to the iron-proxy sidecar); `postgres-secrets`
(POSTGRES_PASSWORD); and `api-secrets` (DATABASE_URL built from that password,
the MCP bearer token, and the IMAP credentials — shared by the `api`,
`mail-indexer`, `gdocs-indexer`, and `api-migrate` workloads).

`bootstrap-iron-proxy-ca` generates a long-lived CA keypair (10y) into the
`iron-proxy-ca` Secret on first run and reuses it on subsequent runs, so the
CA stays stable across helm upgrades. To rotate, delete the Secret and re-run:

```sh
kubectl -n balvibot delete secret iron-proxy-ca
just bootstrap-iron-proxy-ca
kubectl -n balvibot rollout restart deploy/hermes-agent
```

The hermes-agent pod renders `/opt/data/config.yaml` from
`hermesAgent.config` (in `values.yaml`) via an init container on every start,
so no `hermes-agent setup` is needed. The pod boots ready. The gateway API is
reachable at `hermes-agent.balvibot.svc.cluster.local:8642`.

## Hermes Local Model Configuration

Hermes is configured through `hermesAgent.config` in Helm values. The chart
uses Hermes' `custom` provider and expects an OpenAI-compatible chat
completions endpoint, such as `llama-server` at `/v1`.

The committed default is:

```yaml
hermesAgent:
  config:
    model:
      provider: custom
      default: Qwen3.5-9B-Q4_K_M.gguf
      base_url: http://host.docker.internal:8080/v1
      api_key: ""
      api_mode: chat_completions
      context_length: 131072
```

Override `base_url`, `default`, and `context_length` in
`helm/balvibot/values.local.yaml` for your environment. Keep
`context_length` at or below the context window used by the model server.

The chart also sets `HERMES_STREAM_READ_TIMEOUT=1800` and
`HERMES_STREAM_STALE_TIMEOUT=1800` so cluster-routed local LLM endpoints get
the same long stream budget Hermes normally applies to localhost and LAN URLs.

If the model runs on the Mac host, install the LaunchAgent in
`macos-launch-agent/`. It starts `llama-server` on port `8080` with the Qwen
GGUF model used by the default Hermes config.

`base_url` only configures the Hermes model client. When iron-proxy is enabled,
the same endpoint also needs an allowed egress path.

If the model endpoint is a hostname and should flow through iron-proxy, add
the hostname to the custom model allowlist:

```yaml
hermesAgent:
  config:
    model:
      base_url: https://llm.example.com/v1
  customModelEgress:
    allowedDomains:
      - llm.example.com
```

If the model endpoint is a Kubernetes Service, egress gateway, or IP-backed
endpoint, add raw Kubernetes NetworkPolicy egress rules:

```yaml
hermesAgent:
  config:
    model:
      base_url: http://llama-server.models.svc.cluster.local:8080/v1
  customModelEgress:
    networkPolicyEgress:
      - to:
          - namespaceSelector:
              matchLabels:
                kubernetes.io/metadata.name: models
            podSelector:
              matchLabels:
                app.kubernetes.io/name: llama-server
        ports:
          - protocol: TCP
            port: 8080
```

When iron-proxy is enabled, the model endpoint must be reachable through one of
those two paths. Use `customModelEgress.allowedDomains` for proxied hostnames.
Use `customModelEgress.networkPolicyEgress` for direct pod, service, gateway,
or IP routes.

### Local Model Over Tailscale

If the model runs on a Mac or workstation reachable only through Tailscale,
expose it to the cluster through a Tailscale egress gateway or proxy, then point
Hermes at the in-cluster Service that fronts that path.

The model server still needs to listen on an address the Tailscale path can
reach. The LaunchAgent in `macos-launch-agent/` starts `llama-server` with
`--host 0.0.0.0`, so it can accept connections from the Tailscale interface.

Example override:

```yaml
hermesAgent:
  config:
    model:
      base_url: http://balvibot-llama.balvibot.svc.cluster.local:8080/v1
  customModelEgress:
    networkPolicyEgress:
      - to:
          - namespaceSelector:
              matchLabels:
                kubernetes.io/metadata.name: tailscale
            podSelector:
              matchLabels:
                tailscale.com/parent-resource: egress-balvibot
                tailscale.com/parent-resource-type: proxygroup
        ports:
          - protocol: TCP
            port: 8080
```

Adjust the namespace, pod labels, Service name, and port to match your
Tailscale setup. If your Tailscale gateway also needs a control or sidecar port
for forwarding, include that port in the same `networkPolicyEgress` entry.

When `hermesAgent.api.enabled` is true (default), a single
`mcp_servers.balvibot-api` entry is merged into the rendered config and the api
MCP URL + bearer token are exposed as `BALVIBOT_API_MCP_URL` /
`BALVIBOT_API_MCP_TOKEN` env vars (resolved by hermes at runtime). That one
endpoint serves the grantee, mail, and docs tools.

## Managing grantees

Grantees, their sender-email mappings (mail attribution), and their authorized
Drive sources (docs sync) all live in Postgres and are managed with the
`api grantee` CLI. Run it inside the `api` pod, e.g.:

```sh
POD=$(kubectl -n balvibot get pod -l app.kubernetes.io/name=api -o name)
kubectl -n balvibot exec -it "$POD" -- api grantee create acme --name "Acme"
kubectl -n balvibot exec -it "$POD" -- api grantee add-email acme dev@acme.org
kubectl -n balvibot exec -it "$POD" -- api grantee authorize folder <drive-folder-id> --grantee acme
kubectl -n balvibot exec -it "$POD" -- api grantee list
```

`pause`/`resume` toggle whether the gdocs sync loop processes a grantee;
`revoke folder|doc` and `remove-email` undo the corresponding mappings.

The agent's `SOUL.md` lives at `helm/balvibot/SOUL.md` and ships in the
`hermes-agent-config` ConfigMap, mounted read-only at `/opt/data/SOUL.md` via
a `subPath` overlay so the agent cannot rewrite its own system prompt. Bump
it by editing the file and redeploying — the `checksum/config` pod annotation
rolls the pod automatically.

Built-in hermes skills live under `docker/hermes-skills/skills/<skill>/SKILL.md`
and ship as the `balvibot/hermes-skills` image. The hermes-agent pod
pulls that image with an `init-skills` init container and syncs `/skills/.`
into `/opt/data/skills/<hermesAgent.skills.category>/` on the PVC. The default
category is `balvibot`; the whole `/opt/data/skills` tree stays writable so
Hermes can patch bundled skills or create new skills at runtime. Roll the skill
bundle by bumping `hermesAgent.skills.image.tag` in `values.yaml` and running
`just build-hermes-skills upload-hermes-skills deploy`.

## iron-proxy egress firewall

Hermes-agent's egress is funnelled through an [iron-proxy](https://docs.iron.sh)
pod (`templates/iron-proxy.yaml`, rendered from top-level `ironProxy.*` in
`values.yaml`). Three independent mechanisms combine to make the proxy
unbypassable:

1. **dnsConfig override.** The hermes pod sets `dnsPolicy: None` and points
   its `nameservers` at the iron-proxy Service ClusterIP (pinned via
   `ironProxy.clusterIP`, default `10.43.42.42`). iron-proxy's DNS server
   returns its own ClusterIP for every non-passthrough lookup, so a plain
   `connect(api.anthropic.com, 443)` lands transparently on iron-proxy:443.
   `*.svc.cluster.local` and `*.cluster.local` pass through to coredns so
   in-cluster Service resolution still works.
2. **NetworkPolicy.** `templates/network-policies.yaml` denies all hermes
   egress except: iron-proxy (DNS, HTTP, HTTPS) and the in-namespace MCP
   services (mail-indexer:8080, signal-cli:8080). Even if hermes ignores
   its DNS and hard-codes `1.1.1.1`, the CNI drops the packet. A second
   policy locks iron-proxy ingress to hermes pods only. Requires a
   NetworkPolicy-enforcing CNI — k3s ships kube-router for this since
   v1.21, so stock k3s installs work out of the box.
3. **Default-deny allowlist inside iron-proxy.** Even traffic that
   successfully reaches iron-proxy is rejected unless its destination is
   in `ironProxy.allowedDomains` — currently `api.anthropic.com` and
   `api.openai.com`. Expand by editing the list and redeploying.

Hermes trusts the iron-proxy CA via `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`,
`CURL_CA_BUNDLE`, and `NODE_EXTRA_CA_CERTS` (all four set, so MITM verifies
regardless of which TLS stack hermes uses internally). The CA cert is
mounted into the hermes container as a single-file `subPath`; `ca.key` is
projected only into the iron-proxy pod.

iron-proxy also runs a **secret-swap** transform: hermes holds the static
strings `ironproxy-{anthropic,openai}-placeholder` as its API keys, and
iron-proxy matches them in the `authorization` / `x-api-key` headers and
substitutes the real values from `iron-proxy-secrets` before forwarding to
the upstream provider. The real keys never appear in the hermes pod.

To disable the whole pipeline (e.g. to debug a transient proxy issue), set
`ironProxy.enabled=false` in `values.local.yaml` and redeploy; you'll then
also need to bootstrap an `hermes-agent-secrets` that includes the real
`ANTHROPIC_API_KEY` / `OPENAI_API_KEY` directly.

## protonmail-bridge first-time login

The bridge needs an interactive, one-time login. Credentials persist on the PVC.

```sh
POD=$(kubectl -n balvibot get pod -l app.kubernetes.io/name=protonmail-bridge -o name)
kubectl -n balvibot exec -it "$POD" -- /bin/bash

# inside the pod:
bash /protonmail/entrypoint.sh init
login          # enter ProtonMail username + password (and 2FA if enabled)
info           # optional: show the generated bridge credentials
exit
```

Then restart the pod so it boots normally with the stored credentials:

```sh
kubectl -n balvibot delete "$POD"
```

## Signal Configuration

The daemon ships with no account. Link it as a secondary device of an existing
Signal account (you can also register a fresh number with `signal-cli register`
+ `verify`, but linking is faster and avoids burning an SMS code).

```sh
POD=$(kubectl -n balvibot get pod -l app.kubernetes.io/name=signal-cli -o name)
kubectl -n balvibot exec -it "$POD" -- \
    signal-cli -d /data link -n "balvibot"
```

This prints a `sgnl://linkdevice?...` URI. On the phone that owns the Signal
account, open **Settings → Linked devices → Link new device** and either scan
the URI as a QR code (e.g. paste it into <https://qrcode.show> from another
machine and point the phone's camera at the result) or use the system camera.

The command blocks until the link is confirmed on the phone, then writes the
linked-account state to `/data` on the PVC. Restart the pod so the daemon boots
with the new account:

```sh
kubectl -n balvibot delete "$POD"
```

Once linked, configure Hermes with the same linked phone number in E.164 form.
When `hermesAgent.signal.account` is empty, the chart does not emit `SIGNAL_*`
environment variables and Hermes runs without Signal.

```yaml
hermesAgent:
  signal:
    account: "+15551234567"
    allowedUsers: "+15557654321,+15559876543"
    groupAllowedUsers: "GROUP_ID_1,GROUP_ID_2"
    homeChannel: "GROUP_ID_1"
    requireMention: true
```

`allowedUsers` controls which direct-message senders Hermes accepts. Leave it
empty to defer to Hermes' default behavior.

`groupAllowedUsers` controls which Signal groups Hermes accepts. Use a
comma-separated list of group IDs, or `*` for every group. Leave it empty to
disable group handling.

`homeChannel` is the default Signal group or destination for scheduled jobs and
proactive messages.

`requireMention` makes group messages require an explicit mention before Hermes
responds. The custom Hermes image in this repo includes the group mention fix
needed for this setting.

To find group IDs after linking, run `listGroups` against the linked account:

```sh
kubectl -n balvibot exec -it "$POD" -- \
    signal-cli -d /data -a +15551234567 listGroups
```

After changing Signal values, redeploy with `just deploy` so the hermes-agent
pod picks up the rendered environment variables.

## Connecting

From inside the cluster, point apps at:

```
smtp-host: protonmail-bridge.balvibot.svc.cluster.local
smtp-port: 25
imap-host: protonmail-bridge.balvibot.svc.cluster.local
imap-port: 143
```

Use the username/password printed by `info`. The bridge link is unencrypted
(STARTTLS uses the bridge's self-signed cert): keep it cluster-internal.
