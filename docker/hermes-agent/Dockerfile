# Custom hermes-agent image: upstream v2026.5.29.2 with the Signal
# require_mention fix from NousResearch/hermes-agent#36088 woven in.
#
# Upstream lacks group require-mention routing for free-response config, so the
# chart's SIGNAL_REQUIRE_MENTION env var is read but never honored. This image
# applies the PR's two production-code hunks (gateway/config.py and
# gateway/platforms/signal.py) on top of the released tag. The PR's test-file
# changes are intentionally omitted — they don't match this tag's test tree and
# the runtime image doesn't run the suite.
#
# Build context is the repo root (see justfile `_build`).
ARG HERMES_VERSION=v2026.5.29.2
FROM nousresearch/hermes-agent:${HERMES_VERSION}

# The upstream image ships the source tree at /opt/hermes (its WorkingDir), with
# the `gateway` package directly under it, so the PR's `a/gateway/...` paths
# apply with -p1 from there. `patch` isn't installed but `git` is, so use
# `git apply`. --check first fails the build loudly if a future base tag drifts
# out of context rather than silently shipping an unpatched image.
COPY docker/hermes-agent/0001-signal-require-mention.patch /tmp/0001-signal-require-mention.patch
RUN set -eux; \
    cd /opt/hermes; \
    git apply -p1 --check /tmp/0001-signal-require-mention.patch; \
    git apply -p1 /tmp/0001-signal-require-mention.patch; \
    rm -f /tmp/0001-signal-require-mention.patch; \
    # Refresh any stale bytecode so the patched source is what actually runs.
    python3 -m py_compile gateway/config.py gateway/platforms/signal.py
