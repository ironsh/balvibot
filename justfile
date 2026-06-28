set shell := ["bash", "-cu"]
set dotenv-load := true

# Pinned upstream proton-bridge git ref baked into the local image.
protonmail_bridge_version := "v3.19.0"
protonmail_bridge_image := "balvibot/protonmail-bridge"
protonmail_bridge_tag := protonmail_bridge_version

api_image := "balvibot/api"
api_tag := "0.1.0"

hermes_skills_image := "balvibot/hermes-skills"
hermes_skills_tag := "0.2.0"

# Upstream hermes-agent release the custom image is built on. The local tag
# encodes that base plus the require_mention patch (#36088) so a base bump or a
# patch change rolls the pinned tag in values.yaml.
hermes_agent_version := "v2026.5.29.2"
hermes_agent_image := "balvibot/hermes-agent"
hermes_agent_tag := hermes_agent_version + "-mention1"

balvibot_namespace := "balvibot"
balvibot_release := "balvibot"
balvibot_chart := "helm/balvibot"
balvibot_values_local := "helm/balvibot/values.local.yaml"

# On-node OCI registry that locally-built images are pushed to and that k3s
# pulls from. The registry listens only on the node's loopback; `_upload`
# reaches it through an SSH tunnel (see `_tunnel`), so it is never exposed on
# the LAN. The push target string (`registry_host:registry_port`) is also the
# image ref the node resolves, so it must equal `imageRegistry` in
# values.local.yaml.
registry_host := env_var_or_default("BALVIBOT_REGISTRY_HOST", "localhost")
registry_port := env_var_or_default("BALVIBOT_REGISTRY_PORT", "5000")
registry := registry_host + ":" + registry_port

default:
    @just --list

# One-shot bring-up: bootstrap secrets + iron-proxy CA, build & push images to
# the on-node registry, and install the helm chart. Requires BALVIBOT_K3S_NODE
# plus all the BALVIBOT_* secret env vars (see `bootstrap-secrets`), and a registry
# already provisioned on the node. Each push only ships layers the registry is
# missing, so re-running after a small code change transfers little. Make sure
# values.local.yaml sets
# `imageRegistry: {{registry}}` so the cluster pulls what was pushed.
up: bootstrap-secrets bootstrap-iron-proxy-ca ship-protonmail-bridge ship-api ship-hermes-skills ship-hermes-agent deploy

# Install/upgrade the helm release. Grantees are managed via the `api grantee`
# CLI against Postgres (not a file), so there's nothing to inject here.
# values.local.yaml is layered on top of the chart defaults when present
# (gitignored).
deploy:
    @overlay=""; [ -f "{{balvibot_values_local}}" ] && overlay="-f {{balvibot_values_local}}"; \
        helm upgrade --install {{balvibot_release}} {{balvibot_chart}} \
            --namespace {{balvibot_namespace}} --create-namespace $overlay

# Build a docker image pinned to linux/amd64 so it runs on x86_64 k3s nodes
# regardless of the host architecture (e.g. Apple Silicon). Build context is
# always the repo root; Dockerfiles reference paths under docker/<svc>/.
[private]
_build image dockerfile *args:
    docker build \
        --platform linux/amd64 \
        {{args}} \
        -f {{dockerfile}} \
        -t {{image}} \
        .

# Ensure an SSH tunnel from {{registry}} on this host to the registry on the
# node's loopback is open, starting one in the background if not. We detect an
# existing tunnel (or any listener) by probing the local port, so repeated
# `_upload` calls within a run reuse a single tunnel. The forward targets
# 127.0.0.1:{{registry_port}} on the node because that is where the registry
# listens; it is never bound to a routable interface.
[private]
_tunnel:
    @set -eu; \
        [ -n "${BALVIBOT_K3S_NODE:-}" ] || { echo "BALVIBOT_K3S_NODE env var required (e.g. BALVIBOT_K3S_NODE=user@host)" >&2; exit 1; }; \
        if nc -z {{registry_host}} {{registry_port}} >/dev/null 2>&1; then \
            exit 0; \
        fi; \
        echo "opening registry tunnel {{registry}} -> $BALVIBOT_K3S_NODE (127.0.0.1:{{registry_port}})"; \
        ssh -fNL {{registry_port}}:127.0.0.1:{{registry_port}} "$BALVIBOT_K3S_NODE"; \
        for _ in $(seq 1 25); do nc -z {{registry_host}} {{registry_port}} >/dev/null 2>&1 && exit 0; sleep 0.2; done; \
        echo "registry tunnel did not come up on {{registry}}" >&2; exit 1

# Tag a locally built image under the registry prefix and push it through the
# tunnel. The push is content-addressed: only layers the registry does not
# already hold cross the wire, so an unchanged image is a cheap no-op and a
# small code change ships just the top layer. k3s pulls the same ref from the
# node-local registry (`imageRegistry` in values.local.yaml).
[private]
_upload image:
    @set -eu; \
        just _tunnel; \
        docker tag "{{image}}" "{{registry}}/{{image}}"; \
        echo "pushing {{registry}}/{{image}}"; \
        docker push "{{registry}}/{{image}}"

# Build via the named recipe, then push. There is no skip-if-unchanged check:
# the registry already deduplicates by layer digest, so pushing an unchanged
# image only costs a few cheap digest HEAD requests over the tunnel.
[private]
_ship image build_recipe:
    @just {{build_recipe}}
    @just _upload "{{image}}"

# Build the protonmail-bridge image locally from docker/protonmail-bridge.
build-protonmail-bridge version=protonmail_bridge_version tag=protonmail_bridge_tag:
    @just _build "{{protonmail_bridge_image}}:{{tag}}" docker/protonmail-bridge/Dockerfile --build-arg version={{version}}

# Push the locally built protonmail-bridge image to the on-node registry.
upload-protonmail-bridge tag=protonmail_bridge_tag:
    @just _upload "{{protonmail_bridge_image}}:{{tag}}"

# Build the consolidated api image (serves MCP + runs the mail/docs indexers
# and migrations via subcommands).
build-api tag=api_tag:
    @just _build "{{api_image}}:{{tag}}" docker/api/Dockerfile

# Push the locally built api image to the on-node registry.
upload-api tag=api_tag:
    @just _upload "{{api_image}}:{{tag}}"

# Build the offline operator CLI (balvi-approve) for the host. It is an HTTP
# client only (no DB), so it ships as a plain binary, not a container image.
build-approve:
    @cd tools/api && go build -trimpath -ldflags="-s -w" -o ../../dist/balvi-approve ./cmd/approve
    @echo "built dist/balvi-approve"

# Build the hermes-skills image — a tiny busybox-based bundle of chart-built-in
# hermes skills (see docker/hermes-skills/skills/). The hermes-agent pod uses
# it as an init container to populate a read-only overlay mount.
build-hermes-skills tag=hermes_skills_tag:
    @just _build "{{hermes_skills_image}}:{{tag}}" docker/hermes-skills/Dockerfile

# Push the locally built hermes-skills image to the on-node registry.
upload-hermes-skills tag=hermes_skills_tag:
    @just _upload "{{hermes_skills_image}}:{{tag}}"

# Build the custom hermes-agent image: upstream `version` with the
# require_mention patch (#36088) applied via git apply in the Dockerfile.
build-hermes-agent version=hermes_agent_version tag=hermes_agent_tag:
    @just _build "{{hermes_agent_image}}:{{tag}}" docker/hermes-agent/Dockerfile --build-arg HERMES_VERSION={{version}}

# Push the locally built hermes-agent image to the on-node registry.
upload-hermes-agent tag=hermes_agent_tag:
    @just _upload "{{hermes_agent_image}}:{{tag}}"

# Build + conditionally upload helpers used by `up`. Each skips the upload step
# when the build did not change the image ID.
ship-protonmail-bridge:
    @just _ship "{{protonmail_bridge_image}}:{{protonmail_bridge_tag}}" build-protonmail-bridge

ship-api:
    @just _ship "{{api_image}}:{{api_tag}}" build-api

ship-hermes-skills:
    @just _ship "{{hermes_skills_image}}:{{hermes_skills_tag}}" build-hermes-skills

ship-hermes-agent:
    @just _ship "{{hermes_agent_image}}:{{hermes_agent_tag}}" build-hermes-agent

# Create/refresh the Kubernetes Secrets for each service from the operator's
# shell env. Idempotent: re-run after changing values to roll the secret.
# Required env vars (all BALVIBOT_-prefixed):
#   hermes-agent:  BALVIBOT_API_SERVER_KEY
#   iron-proxy:    at least one of BALVIBOT_ANTHROPIC_API_KEY / BALVIBOT_OPENAI_API_KEY
#                  (real LLM keys — hermes itself never sees them)
#   postgres:      BALVIBOT_POSTGRES_PASSWORD (URL-safe; goes into DATABASE_URL)
#   api:           BALVIBOT_IMAP_USER, BALVIBOT_IMAP_PASS, BALVIBOT_API_MCP_TOKEN
#   api (optional): BALVIBOT_APPROVAL_BOOTSTRAP_EMAIL + BALVIBOT_APPROVAL_BOOTSTRAP_PUBKEY
#                  (an authorized_keys line) seed the first approval operator on
#                  `api migrate up`; the fingerprint is derived from the key.
#   iron-proxy gcp_auth: BALVIBOT_GCP_SA_KEY_FILE (path to the SA JSON keyfile;
#                        only iron-proxy sees it, gdocs-indexer never does)
bootstrap-secrets:
    @set -eu; \
        missing=(); \
        for v in BALVIBOT_API_SERVER_KEY BALVIBOT_POSTGRES_PASSWORD BALVIBOT_IMAP_USER BALVIBOT_IMAP_PASS BALVIBOT_API_MCP_TOKEN BALVIBOT_GCP_SA_KEY_FILE; do \
            if [ -z "${!v:-}" ]; then missing+=("$v"); fi; \
        done; \
        if [ -n "${BALVIBOT_GCP_SA_KEY_FILE:-}" ] && [ ! -f "$BALVIBOT_GCP_SA_KEY_FILE" ]; then \
            echo "BALVIBOT_GCP_SA_KEY_FILE points to non-existent file: $BALVIBOT_GCP_SA_KEY_FILE" >&2; exit 1; \
        fi; \
        if [ -z "${BALVIBOT_ANTHROPIC_API_KEY:-}" ] && [ -z "${BALVIBOT_OPENAI_API_KEY:-}" ]; then \
            missing+=("BALVIBOT_ANTHROPIC_API_KEY or BALVIBOT_OPENAI_API_KEY"); \
        fi; \
        if [ "${#missing[@]}" -gt 0 ]; then \
            echo "missing required env vars: ${missing[*]}" >&2; exit 1; \
        fi; \
        iron_args=(); \
        if [ -n "${BALVIBOT_ANTHROPIC_API_KEY:-}" ]; then \
            iron_args+=(--from-literal=REAL_ANTHROPIC_API_KEY="$BALVIBOT_ANTHROPIC_API_KEY"); \
        fi; \
        if [ -n "${BALVIBOT_OPENAI_API_KEY:-}" ]; then \
            iron_args+=(--from-literal=REAL_OPENAI_API_KEY="$BALVIBOT_OPENAI_API_KEY"); \
        fi; \
        kubectl create namespace {{balvibot_namespace}} --dry-run=client -o yaml | kubectl apply -f -; \
        kubectl create secret generic hermes-agent-secrets \
            --namespace={{balvibot_namespace}} \
            --from-literal=API_SERVER_KEY="$BALVIBOT_API_SERVER_KEY" \
            --dry-run=client -o yaml | kubectl apply -f -; \
        kubectl create secret generic iron-proxy-secrets \
            --namespace={{balvibot_namespace}} \
            "${iron_args[@]}" \
            --dry-run=client -o yaml | kubectl apply -f -; \
        kubectl create secret generic postgres-secrets \
            --namespace={{balvibot_namespace}} \
            --from-literal=POSTGRES_PASSWORD="$BALVIBOT_POSTGRES_PASSWORD" \
            --dry-run=client -o yaml | kubectl apply -f -; \
        database_url="postgres://balvibot:${BALVIBOT_POSTGRES_PASSWORD}@postgres.{{balvibot_namespace}}.svc.cluster.local:5432/balvibot?sslmode=disable"; \
        kubectl create secret generic api-secrets \
            --namespace={{balvibot_namespace}} \
            --from-literal=DATABASE_URL="$database_url" \
            --from-literal=MCP_BEARER_TOKEN="$BALVIBOT_API_MCP_TOKEN" \
            --from-literal=APPROVAL_BOOTSTRAP_EMAIL="${BALVIBOT_APPROVAL_BOOTSTRAP_EMAIL:-}" \
            --from-literal=APPROVAL_BOOTSTRAP_PUBKEY="${BALVIBOT_APPROVAL_BOOTSTRAP_PUBKEY:-}" \
            --from-literal=IMAP_USER="$BALVIBOT_IMAP_USER" \
            --from-literal=IMAP_PASS="$BALVIBOT_IMAP_PASS" \
            --dry-run=client -o yaml | kubectl apply -f -; \
        kubectl create secret generic gdrive-sa \
            --namespace={{balvibot_namespace}} \
            --from-file=key.json="$BALVIBOT_GCP_SA_KEY_FILE" \
            --dry-run=client -o yaml | kubectl apply -f -

# Generate the iron-proxy CA keypair on first run and store it in the
# `iron-proxy-ca` Secret. Skipped if the Secret already exists, so re-running
# is safe and the CA stays stable across helm upgrades (any drift would force
# every workload to re-trust a new cert). To rotate, delete the Secret first.
bootstrap-iron-proxy-ca:
    @set -eu; \
        kubectl create namespace {{balvibot_namespace}} --dry-run=client -o yaml | kubectl apply -f -; \
        if kubectl -n {{balvibot_namespace}} get secret iron-proxy-ca >/dev/null 2>&1; then \
            echo "iron-proxy-ca secret already exists; reusing"; \
            exit 0; \
        fi; \
        tmpdir=$(mktemp -d); \
        trap 'rm -rf "$tmpdir"' EXIT; \
        openssl genrsa -out "$tmpdir/ca.key" 4096 >/dev/null 2>&1; \
        openssl req -x509 -new -nodes \
            -key "$tmpdir/ca.key" -sha256 -days 3650 \
            -subj "/CN=balvibot iron-proxy CA" \
            -addext "basicConstraints=critical,CA:TRUE" \
            -addext "keyUsage=critical,keyCertSign" \
            -out "$tmpdir/ca.crt" >/dev/null 2>&1; \
        kubectl -n {{balvibot_namespace}} create secret generic iron-proxy-ca \
            --from-file=ca.crt="$tmpdir/ca.crt" \
            --from-file=ca.key="$tmpdir/ca.key"; \
        echo "iron-proxy-ca created (CN=balvibot iron-proxy CA, 10y)"
