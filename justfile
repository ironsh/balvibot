set shell := ["bash", "-cu"]
set dotenv-load := true

# Pinned upstream proton-bridge git ref baked into the local image.
protonmail_bridge_version := "v3.19.0"
protonmail_bridge_image := "philanthropy-os/protonmail-bridge"
protonmail_bridge_tag := protonmail_bridge_version

mail_indexer_image := "philanthropy-os/mail-indexer"
mail_indexer_tag := "0.1.0"

signal_cli_version := "0.14.3"
signal_cli_image := "philanthropy-os/signal-cli"
signal_cli_tag := signal_cli_version

hermes_skills_image := "philanthropy-os/hermes-skills"
hermes_skills_tag := "0.2.0"

philos_namespace := "philanthropy-os"
philos_release := "philanthropy-os"
philos_chart := "helm/philanthropy-os"
philos_grantees := "helm/philanthropy-os/grantees.json"
philos_values_local := "helm/philanthropy-os/values.local.yaml"

default:
    @just --list

# One-shot bring-up: bootstrap secrets, build & ship both images to the remote
# k3s node, and install the helm chart. Requires PHILOS_K3S_NODE plus all the
# PHILOS_* secret env vars (see `bootstrap-secrets`).
up: bootstrap-secrets ship-protonmail-bridge ship-mail-indexer ship-signal-cli ship-hermes-skills deploy

# Install/upgrade the helm release. grantees.json is injected via --set-file so
# the PII never lands in values.yaml or git. values.local.yaml is layered on top
# of the chart defaults when present (also gitignored).
deploy:
    @[ -f "{{philos_grantees}}" ] || { echo "missing {{philos_grantees}} (copy {{philos_grantees}}.example and edit)" >&2; exit 1; }
    @overlay=""; [ -f "{{philos_values_local}}" ] && overlay="-f {{philos_values_local}}"; \
        helm upgrade --install {{philos_release}} {{philos_chart}} \
            --namespace {{philos_namespace}} --create-namespace \
            --set-file mailIndexer.grantees={{philos_grantees}} $overlay

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
# the node's k3s containerd image store.
[private]
_upload image:
    @[ -n "${PHILOS_K3S_NODE:-}" ] || { echo "PHILOS_K3S_NODE env var required (e.g. PHILOS_K3S_NODE=user@host)" >&2; exit 1; }
    docker save {{image}} | ssh "$PHILOS_K3S_NODE" 'sudo k3s ctr images import -'

# Build via the named recipe and only upload to the k3s node if the resulting
# image ID actually changed. Lets `up` be a cheap no-op when nothing rebuilds.
[private]
_ship image build_recipe:
    @set -eu; \
        prev=$(docker images -q "{{image}}" 2>/dev/null || true); \
        just {{build_recipe}}; \
        curr=$(docker images -q "{{image}}" 2>/dev/null || true); \
        if [ -n "$prev" ] && [ "$prev" = "$curr" ]; then \
            echo "{{image}} unchanged ($curr); skipping upload"; \
        else \
            just _upload "{{image}}"; \
        fi

# Build the protonmail-bridge image locally from docker/protonmail-bridge.
build-protonmail-bridge version=protonmail_bridge_version tag=protonmail_bridge_tag:
    @just _build "{{protonmail_bridge_image}}:{{tag}}" docker/protonmail-bridge/Dockerfile --build-arg version={{version}}

# Stream the locally built protonmail-bridge image to the remote k3s node.
upload-protonmail-bridge tag=protonmail_bridge_tag:
    @just _upload "{{protonmail_bridge_image}}:{{tag}}"

# Build the mail-indexer image.
build-mail-indexer tag=mail_indexer_tag:
    @just _build "{{mail_indexer_image}}:{{tag}}" docker/mail-indexer/Dockerfile

# Stream the locally built mail-indexer image to the remote k3s node.
upload-mail-indexer tag=mail_indexer_tag:
    @just _upload "{{mail_indexer_image}}:{{tag}}"

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

# Build + conditionally upload helpers used by `up`. Each skips the upload step
# when the build did not change the image ID.
ship-protonmail-bridge:
    @just _ship "{{protonmail_bridge_image}}:{{protonmail_bridge_tag}}" build-protonmail-bridge

ship-mail-indexer:
    @just _ship "{{mail_indexer_image}}:{{mail_indexer_tag}}" build-mail-indexer

ship-signal-cli:
    @just _ship "{{signal_cli_image}}:{{signal_cli_tag}}" build-signal-cli

ship-hermes-skills:
    @just _ship "{{hermes_skills_image}}:{{hermes_skills_tag}}" build-hermes-skills

# Create/refresh the Kubernetes Secrets for each service from the operator's
# shell env. Idempotent: re-run after changing values to roll the secret.
# Required env vars (all PHILOS_-prefixed):
#   hermes-agent: PHILOS_API_SERVER_KEY + at least one of
#                 PHILOS_ANTHROPIC_API_KEY / PHILOS_OPENAI_API_KEY
#   mail-indexer: PHILOS_IMAP_USER, PHILOS_IMAP_PASS, PHILOS_MAIL_INDEXER_MCP_TOKEN
bootstrap-secrets:
    @set -eu; \
        missing=(); \
        for v in PHILOS_API_SERVER_KEY PHILOS_IMAP_USER PHILOS_IMAP_PASS PHILOS_MAIL_INDEXER_MCP_TOKEN; do \
            if [ -z "${!v:-}" ]; then missing+=("$v"); fi; \
        done; \
        if [ -z "${PHILOS_ANTHROPIC_API_KEY:-}" ] && [ -z "${PHILOS_OPENAI_API_KEY:-}" ]; then \
            missing+=("PHILOS_ANTHROPIC_API_KEY or PHILOS_OPENAI_API_KEY"); \
        fi; \
        if [ "${#missing[@]}" -gt 0 ]; then \
            echo "missing required env vars: ${missing[*]}" >&2; exit 1; \
        fi; \
        hermes_args=(--from-literal=API_SERVER_KEY="$PHILOS_API_SERVER_KEY"); \
        if [ -n "${PHILOS_ANTHROPIC_API_KEY:-}" ]; then \
            hermes_args+=(--from-literal=ANTHROPIC_API_KEY="$PHILOS_ANTHROPIC_API_KEY"); \
        fi; \
        if [ -n "${PHILOS_OPENAI_API_KEY:-}" ]; then \
            hermes_args+=(--from-literal=OPENAI_API_KEY="$PHILOS_OPENAI_API_KEY"); \
        fi; \
        kubectl create namespace {{philos_namespace}} --dry-run=client -o yaml | kubectl apply -f -; \
        kubectl create secret generic hermes-agent-secrets \
            --namespace={{philos_namespace}} \
            "${hermes_args[@]}" \
            --dry-run=client -o yaml | kubectl apply -f -; \
        kubectl create secret generic mail-indexer-secrets \
            --namespace={{philos_namespace}} \
            --from-literal=IMAP_USER="$PHILOS_IMAP_USER" \
            --from-literal=IMAP_PASS="$PHILOS_IMAP_PASS" \
            --from-literal=MCP_BEARER_TOKEN="$PHILOS_MAIL_INDEXER_MCP_TOKEN" \
            --dry-run=client -o yaml | kubectl apply -f -
