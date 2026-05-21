# philanthropy-os (Kustomize)

Kubernetes manifests for the philanthropy-os services.

## Services

- **protonmail-bridge** — locally built image (see `docker/protonmail-bridge/`,
  ported from [`shenxn/protonmail-bridge-docker`](https://github.com/shenxn/protonmail-bridge-docker))
  exposing local SMTP (25) and IMAP (143) endpoints that proxy a ProtonMail
  account so cluster workloads can send/receive mail.
- **hermes-agent** — runs [`nousresearch/hermes-agent`](https://hermes-agent.nousresearch.com/docs/user-guide/docker)
  in gateway mode, exposing an OpenAI-compatible API (8642) and dashboard (9119).

## Layout

```
kustomize/services/<name>/base/   Reusable manifests for one service
kustomize/overlays/dev/           Environment overlay: composes all services into a namespace
```

The `protonmail-bridge` service lives at `kustomize/services/protonmail-bridge/base`.
Add new services as sibling folders under `kustomize/services/` and reference
each service's `base` from the environment overlays.

## Deploy

Build the protonmail-bridge image first (it's referenced by tag, not pulled):

```sh
just build-protonmail-bridge
```

Then apply the manifests:

```sh
kubectl apply -k kustomize/overlays/dev
```

If your cluster is remote, load the image into it (e.g. `kind load
docker-image philanthropy-os/protonmail-bridge:v3.19.0`, `minikube image load
…`, or push to your registry and override the image in an overlay).

## hermes-agent secrets

Before deploying, create the secret env file from the template:

```sh
cd kustomize/services/hermes-agent/base
cp secrets.env.example secrets.env
# fill in API_SERVER_KEY (openssl rand -hex 32) and provider API keys
```

`secrets.env` is gitignored and consumed by the kustomize `secretGenerator`.

After first deploy, populate the data volume (`config.yaml`, `SOUL.md`) by
running the agent's interactive setup:

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
