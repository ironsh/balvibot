# Tiny image whose only purpose is to ship chart-bundled hermes skills into the
# pod. The `init-skills` init container in the hermes-agent Deployment copies
# /skills/. into an emptyDir, which is then mounted read-only over
# /opt/data/skills/<category>/ in the main container — an overlay over the PVC,
# so user/agent-authored skills in other categories remain writable.
#
# busybox is the base (~4MB) so the init container has a shell for `cp -a`.
FROM busybox:1.36
COPY docker/hermes-skills/skills/ /skills/
