# Tiny image whose only purpose is to ship chart-bundled hermes skills into the
# pod. The `init-skills` init container in the hermes-agent Deployment copies
# /skills/. into /opt/data/skills/<category>/ on the Hermes PVC, then keeps the
# skills tree writable so user/agent-authored skills in other categories remain
# mutable at runtime.
#
# busybox is the base (~4MB) so the init container has a shell for `cp -a`.
FROM busybox:1.36
COPY docker/hermes-skills/skills/ /skills/
