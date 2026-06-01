set shell := ["bash", "-cu"]
set dotenv-load := true

# Pinned upstream proton-bridge git ref baked into the local image.
protonmail_bridge_version := "v3.19.0"
protonmail_bridge_image := "philanthropy-os/protonmail-bridge"
protonmail_bridge_tag := protonmail_bridge_version

api_image := "philanthropy-os/api"
api_tag := "0.1.0"

signal_cli_version := "0.14.3"
signal_cli_image := "philanthropy-os/signal-cli"
signal_cli_tag := signal_cli_version

hermes_skills_image := "philanthropy-os/hermes-skills"
hermes_skills_tag := "0.2.0"

# Upstream hermes-agent release the custom image is built on. The local tag
# encodes that base plus the require_mention patch (#36088) so a base bump or a
# patch change rolls the pinned tag in values.yaml.
hermes_agent_version := "v2026.5.29.2"
hermes_agent_image := "philanthropy-os/hermes-agent"
hermes_agent_tag := hermes_agent_version + "-mention1"

philos_namespace := "philanthropy-os"
philos_release := "philanthropy-os"
philos_chart := "helm/philanthropy-os"
philos_values_local := "helm/philanthropy-os/values.local.yaml"

default:
    @just --list

# One-shot bring-up: bootstrap secrets + iron-proxy CA, build & ship images
# to the remote k3s node, and install the helm chart. Requires PHILOS_K3S_NODE
# plus all the PHILOS_* secret env vars (see `bootstrap-secrets`). Set
# PHILOS_FORCE_SHIP=1 to re-upload every image even if the node already has it.
up: bootstrap-secrets bootstrap-iron-proxy-ca ship-protonmail-bridge ship-api ship-signal-cli ship-hermes-skills ship-hermes-agent deploy

# Install/upgrade the helm release. Grantees are managed via the `api grantee`
# CLI against Postgres (not a file), so there's nothing to inject here.
# values.local.yaml is layered on top of the chart defaults when present
# (gitignored).
deploy:
    @overlay=""; [ -f "{{philos_values_local}}" ] && overlay="-f {{philos_values_local}}"; \
        helm upgrade --install {{philos_release}} {{philos_chart}} \
            --namespace {{philos_namespace}} --create-namespace $overlay

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

# Stream a locally built image over SSH to $PHILOS_K3S_NODE and import it into
# the node's k3s containerd image store. After import, stamp the local docker
# image ID onto the image as a `balvi.image-id` label so `_ship` can later tell,
# from the node's own store, whether it already holds this exact build.
[private]
_upload image:
    @set -eu; \
        [ -n "${PHILOS_K3S_NODE:-}" ] || { echo "PHILOS_K3S_NODE env var required (e.g. PHILOS_K3S_NODE=user@host)" >&2; exit 1; }; \
        local_id=$(docker image inspect --format '{{{{.Id}}' "{{image}}"); \
        docker save "{{image}}" | ssh "$PHILOS_K3S_NODE" 'sudo k3s ctr images import -'; \
        ssh "$PHILOS_K3S_NODE" "sudo k3s ctr images label '{{image}}' balvi.image-id='$local_id'" >/dev/null

# Build via the named recipe, then upload to the k3s node only if the node's
# containerd store does not already hold this exact image, matched by the
# balvi.image-id label _upload stamps on import. This consults the remote truth
# rather than the local docker cache, so a node that is missing the image (fresh
# node, pruned store) still gets it even when the local build is unchanged. Set
# PHILOS_FORCE_SHIP=1 to skip the check and always upload.
[private]
_ship image build_recipe:
    @set -eu; \
        just {{build_recipe}}; \
        if [ -n "${PHILOS_FORCE_SHIP:-}" ]; then \
            echo "PHILOS_FORCE_SHIP set; forcing upload of {{image}}"; \
            just _upload "{{image}}"; \
        else \
            [ -n "${PHILOS_K3S_NODE:-}" ] || { echo "PHILOS_K3S_NODE env var required (e.g. PHILOS_K3S_NODE=user@host)" >&2; exit 1; }; \
            local_id=$(docker image inspect --format '{{{{.Id}}' "{{image}}"); \
            filter="name==\"{{image}}\",labels.\"balvi.image-id\"==\"$local_id\""; \
            match=$(ssh "$PHILOS_K3S_NODE" "sudo k3s ctr images ls '$filter' -q" 2>/dev/null || true); \
            if [ -n "$match" ]; then \
                echo "{{image}} already on k3s node ($local_id); skipping upload"; \
            else \
                just _upload "{{image}}"; \
            fi; \
        fi

# Build the protonmail-bridge image locally from docker/protonmail-bridge.
build-protonmail-bridge version=protonmail_bridge_version tag=protonmail_bridge_tag:
    @just _build "{{protonmail_bridge_image}}:{{tag}}" docker/protonmail-bridge/Dockerfile --build-arg version={{version}}

# Stream the locally built protonmail-bridge image to the remote k3s node.
upload-protonmail-bridge tag=protonmail_bridge_tag:
    @just _upload "{{protonmail_bridge_image}}:{{tag}}"

# Build the consolidated api image (serves MCP + runs the mail/docs indexers
# and migrations via subcommands).
build-api tag=api_tag:
    @just _build "{{api_image}}:{{tag}}" docker/api/Dockerfile

# Stream the locally built api image to the remote k3s node.
upload-api tag=api_tag:
    @just _upload "{{api_image}}:{{tag}}"

# Build the offline operator CLI (balvi-approve) for the host. It is an HTTP
# client only (no DB), so it ships as a plain binary, not a container image.
build-approve:
    @cd tools/api && go build -trimpath -ldflags="-s -w" -o ../../dist/balvi-approve ./cmd/approve
    @echo "built dist/balvi-approve"

# Build the signal-cli image. The Dockerfile pulls the pinned upstream tarball
# from github.com/AsamK/signal-cli at build time.
build-signal-cli version=signal_cli_version tag=signal_cli_tag:
    @just _build "{{signal_cli_image}}:{{tag}}" docker/signal-cli/Dockerfile --build-arg version={{version}}

# Stream the locally built signal-cli image to the remote k3s node.
upload-signal-cli tag=signal_cli_tag:
    @just _upload "{{signal_cli_image}}:{{tag}}"

# Build the hermes-skills image — a tiny busybox-based bundle of chart-built-in
# hermes skills (see docker/hermes-skills/skills/). The hermes-agent pod uses
# it as an init container to populate a read-only overlay mount.
build-hermes-skills tag=hermes_skills_tag:
    @just _build "{{hermes_skills_image}}:{{tag}}" docker/hermes-skills/Dockerfile

# Stream the locally built hermes-skills image to the remote k3s node.
upload-hermes-skills tag=hermes_skills_tag:
    @just _upload "{{hermes_skills_image}}:{{tag}}"

# Build the custom hermes-agent image: upstream `version` with the
# require_mention patch (#36088) applied via git apply in the Dockerfile.
build-hermes-agent version=hermes_agent_version tag=hermes_agent_tag:
    @just _build "{{hermes_agent_image}}:{{tag}}" docker/hermes-agent/Dockerfile --build-arg HERMES_VERSION={{version}}

# Stream the locally built hermes-agent image to the remote k3s node.
upload-hermes-agent tag=hermes_agent_tag:
    @just _upload "{{hermes_agent_image}}:{{tag}}"

# Build + conditionally upload helpers used by `up`. Each skips the upload step
# when the build did not change the image ID.
ship-protonmail-bridge:
    @just _ship "{{protonmail_bridge_image}}:{{protonmail_bridge_tag}}" build-protonmail-bridge

ship-api:
    @just _ship "{{api_image}}:{{api_tag}}" build-api

ship-signal-cli:
    @just _ship "{{signal_cli_image}}:{{signal_cli_tag}}" build-signal-cli

ship-hermes-skills:
    @just _ship "{{hermes_skills_image}}:{{hermes_skills_tag}}" build-hermes-skills

ship-hermes-agent:
    @just _ship "{{hermes_agent_image}}:{{hermes_agent_tag}}" build-hermes-agent

# Create/refresh the Kubernetes Secrets for each service from the operator's
# shell env. Idempotent: re-run after changing values to roll the secret.
# Required env vars (all PHILOS_-prefixed):
#   hermes-agent:  PHILOS_API_SERVER_KEY
#   iron-proxy:    at least one of PHILOS_ANTHROPIC_API_KEY / PHILOS_OPENAI_API_KEY
#                  (real LLM keys — hermes itself never sees them)
#   postgres:      PHILOS_POSTGRES_PASSWORD (URL-safe; goes into DATABASE_URL)
#   api:           PHILOS_IMAP_USER, PHILOS_IMAP_PASS, PHILOS_API_MCP_TOKEN
#   api (optional): PHILOS_APPROVAL_BOOTSTRAP_EMAIL + PHILOS_APPROVAL_BOOTSTRAP_PUBKEY
#                  (an authorized_keys line) seed the first approval operator on
#                  `api migrate up`; the fingerprint is derived from the key.
#   iron-proxy gcp_auth: PHILOS_GCP_SA_KEY_FILE (path to the SA JSON keyfile;
#                        only iron-proxy sees it, gdocs-indexer never does)
bootstrap-secrets:
    @set -eu; \
        missing=(); \
        for v in PHILOS_API_SERVER_KEY PHILOS_POSTGRES_PASSWORD PHILOS_IMAP_USER PHILOS_IMAP_PASS PHILOS_API_MCP_TOKEN PHILOS_GCP_SA_KEY_FILE; do \
            if [ -z "${!v:-}" ]; then missing+=("$v"); fi; \
        done; \
        if [ -n "${PHILOS_GCP_SA_KEY_FILE:-}" ] && [ ! -f "$PHILOS_GCP_SA_KEY_FILE" ]; then \
            echo "PHILOS_GCP_SA_KEY_FILE points to non-existent file: $PHILOS_GCP_SA_KEY_FILE" >&2; exit 1; \
        fi; \
        if [ -z "${PHILOS_ANTHROPIC_API_KEY:-}" ] && [ -z "${PHILOS_OPENAI_API_KEY:-}" ]; then \
            missing+=("PHILOS_ANTHROPIC_API_KEY or PHILOS_OPENAI_API_KEY"); \
        fi; \
        if [ "${#missing[@]}" -gt 0 ]; then \
            echo "missing required env vars: ${missing[*]}" >&2; exit 1; \
        fi; \
        iron_args=(); \
        if [ -n "${PHILOS_ANTHROPIC_API_KEY:-}" ]; then \
            iron_args+=(--from-literal=REAL_ANTHROPIC_API_KEY="$PHILOS_ANTHROPIC_API_KEY"); \
        fi; \
        if [ -n "${PHILOS_OPENAI_API_KEY:-}" ]; then \
            iron_args+=(--from-literal=REAL_OPENAI_API_KEY="$PHILOS_OPENAI_API_KEY"); \
        fi; \
        kubectl create namespace {{philos_namespace}} --dry-run=client -o yaml | kubectl apply -f -; \
        kubectl create secret generic hermes-agent-secrets \
            --namespace={{philos_namespace}} \
            --from-literal=API_SERVER_KEY="$PHILOS_API_SERVER_KEY" \
            --dry-run=client -o yaml | kubectl apply -f -; \
        kubectl create secret generic iron-proxy-secrets \
            --namespace={{philos_namespace}} \
            "${iron_args[@]}" \
            --dry-run=client -o yaml | kubectl apply -f -; \
        kubectl create secret generic postgres-secrets \
            --namespace={{philos_namespace}} \
            --from-literal=POSTGRES_PASSWORD="$PHILOS_POSTGRES_PASSWORD" \
            --dry-run=client -o yaml | kubectl apply -f -; \
        database_url="postgres://philos:${PHILOS_POSTGRES_PASSWORD}@postgres.{{philos_namespace}}.svc.cluster.local:5432/philos?sslmode=disable"; \
        kubectl create secret generic api-secrets \
            --namespace={{philos_namespace}} \
            --from-literal=DATABASE_URL="$database_url" \
            --from-literal=MCP_BEARER_TOKEN="$PHILOS_API_MCP_TOKEN" \
            --from-literal=APPROVAL_BOOTSTRAP_EMAIL="${PHILOS_APPROVAL_BOOTSTRAP_EMAIL:-}" \
            --from-literal=APPROVAL_BOOTSTRAP_PUBKEY="${PHILOS_APPROVAL_BOOTSTRAP_PUBKEY:-}" \
            --from-literal=IMAP_USER="$PHILOS_IMAP_USER" \
            --from-literal=IMAP_PASS="$PHILOS_IMAP_PASS" \
            --dry-run=client -o yaml | kubectl apply -f -; \
        kubectl create secret generic gdrive-sa \
            --namespace={{philos_namespace}} \
            --from-file=key.json="$PHILOS_GCP_SA_KEY_FILE" \
            --dry-run=client -o yaml | kubectl apply -f -

# Generate the iron-proxy CA keypair on first run and store it in the
# `iron-proxy-ca` Secret. Skipped if the Secret already exists, so re-running
# is safe and the CA stays stable across helm upgrades (any drift would force
# every workload to re-trust a new cert). To rotate, delete the Secret first.
bootstrap-iron-proxy-ca:
    @set -eu; \
        kubectl create namespace {{philos_namespace}} --dry-run=client -o yaml | kubectl apply -f -; \
        if kubectl -n {{philos_namespace}} get secret iron-proxy-ca >/dev/null 2>&1; then \
            echo "iron-proxy-ca secret already exists; reusing"; \
            exit 0; \
        fi; \
        tmpdir=$(mktemp -d); \
        trap 'rm -rf "$tmpdir"' EXIT; \
        openssl genrsa -out "$tmpdir/ca.key" 4096 >/dev/null 2>&1; \
        openssl req -x509 -new -nodes \
            -key "$tmpdir/ca.key" -sha256 -days 3650 \
            -subj "/CN=philanthropy-os iron-proxy CA" \
            -addext "basicConstraints=critical,CA:TRUE" \
            -addext "keyUsage=critical,keyCertSign" \
            -out "$tmpdir/ca.crt" >/dev/null 2>&1; \
        kubectl -n {{philos_namespace}} create secret generic iron-proxy-ca \
            --from-file=ca.crt="$tmpdir/ca.crt" \
            --from-file=ca.key="$tmpdir/ca.key"; \
        echo "iron-proxy-ca created (CN=philanthropy-os iron-proxy CA, 10y)"
