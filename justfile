set shell := ["bash", "-cu"]

# Pinned upstream proton-bridge git ref baked into the local image.
protonmail_bridge_version := "v3.19.0"
protonmail_bridge_image := "philanthropy-os/protonmail-bridge"
protonmail_bridge_tag := protonmail_bridge_version

default:
    @just --list

# Build the protonmail-bridge image locally from docker/protonmail-bridge.
# Pinned to linux/amd64 so the image runs on x86_64 k3s nodes regardless of
# the host architecture (e.g. Apple Silicon).
build-protonmail-bridge version=protonmail_bridge_version tag=protonmail_bridge_tag:
    docker build \
        --platform linux/amd64 \
        --build-arg version={{version}} \
        -t {{protonmail_bridge_image}}:{{tag}} \
        docker/protonmail-bridge

# Stream the locally built protonmail-bridge image over SSH to $PHILOS_K3S_NODE
# and import it into the node's k3s containerd image store.
upload-protonmail-bridge tag=protonmail_bridge_tag:
    @[ -n "${PHILOS_K3S_NODE:-}" ] || { echo "PHILOS_K3S_NODE env var required (e.g. PHILOS_K3S_NODE=user@host)" >&2; exit 1; }
    docker save {{protonmail_bridge_image}}:{{tag}} \
        | ssh "$PHILOS_K3S_NODE" 'sudo k3s ctr images import -'
