# philanthropy-os (Helm)

Kubernetes manifests for the philanthropy-os services, packaged as a Helm chart.

## Services

- **protonmail-bridge** — locally built image (see `docker/protonmail-bridge/`,
  ported from [`shenxn/protonmail-bridge-docker`](https://github.com/shenxn/protonmail-bridge-docker))
  exposing local SMTP (25) and IMAP (143) endpoints that proxy a ProtonMail
  account so cluster workloads can send/receive mail.
- **mail-indexer** — indexes mail from the bridge into SQLite, tagging messages
  by grantee (see `helm/philanthropy-os/grantees.json`).
- **hermes-agent** — runs [`nousresearch/hermes-agent`](https://hermes-agent.nousresearch.com/docs/user-guide/docker)
  in gateway mode, exposing an OpenAI-compatible API (8642) and dashboard (9119).

## Layout

```
helm/philanthropy-os/Chart.yaml      Chart metadata
helm/philanthropy-os/values.yaml     Tunables (image tags, env, resources)
helm/philanthropy-os/templates/      One file per service
```

Each service template renders its Deployment, Service (where applicable), and
PVC. Add a new service by dropping a new template file alongside the existing
ones and a matching block in `values.yaml`.

## Deploy

Build the local images first (they're referenced by tag, not pulled):

```sh
just build-protonmail-bridge
just build-mail-indexer
```

Copy and edit the grantee mapping (gitignored):

```sh
cp helm/philanthropy-os/grantees.json.example helm/philanthropy-os/grantees.json
# edit grantees.json
```

Install/upgrade the chart:

```sh
just deploy
```

This runs `helm upgrade --install philanthropy-os helm/philanthropy-os
--namespace philanthropy-os --create-namespace --set-file
mailIndexer.grantees=helm/philanthropy-os/grantees.json`.

If your cluster is remote, load the locally built images into it (the justfile
does this for you over SSH via `just upload-protonmail-bridge` / `just
upload-mail-indexer`, or use `kind load docker-image …`, `minikube image load
…`, etc.).

## Secrets

Secrets are provisioned out-of-band from a `.env` file (gitignored, auto-loaded
by the justfile). Bootstrap once from `.env.example`:

```sh
cp .env.example .env
# edit .env, fill in PHILOS_* values

just bootstrap-secrets
```

The recipe is idempotent — re-run it any time to roll a value.

After first deploy, populate the hermes-agent data volume (`config.yaml`,
`SOUL.md`) by running the agent's interactive setup:

```sh
POD=$(kubectl -n philanthropy-os get pod -l app.kubernetes.io/name=hermes-agent -o name)
kubectl -n philanthropy-os exec -it "$POD" -- hermes-agent setup
```

The gateway API is then reachable at
`hermes-agent.philanthropy-os.svc.cluster.local:8642`.

## protonmail-bridge first-time login

The bridge needs an interactive, one-time login. Credentials persist on the PVC.

```sh
POD=$(kubectl -n philanthropy-os get pod -l app.kubernetes.io/name=protonmail-bridge -o name)
kubectl -n philanthropy-os exec -it "$POD" -- /bin/bash

# inside the pod:
bash /protonmail/entrypoint.sh init
login          # enter ProtonMail username + password (and 2FA if enabled)
info           # optional: show the generated bridge credentials
exit
```

Then restart the pod so it boots normally with the stored credentials:

```sh
kubectl -n philanthropy-os delete "$POD"
```

## Connecting

From inside the cluster, point apps at:

```
smtp-host: protonmail-bridge.philanthropy-os.svc.cluster.local
smtp-port: 25
imap-host: protonmail-bridge.philanthropy-os.svc.cluster.local
imap-port: 143
```

Use the username/password printed by `info`. The bridge link is unencrypted
(STARTTLS uses the bridge's self-signed cert): keep it cluster-internal.
