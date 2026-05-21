FROM eclipse-temurin:26-jre-noble

ARG version

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

# Install signal-cli from the upstream tarball. The release ships a fat-jar
# wrapper and a `bin/signal-cli` launcher under signal-cli-${version}/.
RUN curl -fsSL "https://github.com/AsamK/signal-cli/releases/download/v${version}/signal-cli-${version}.tar.gz" \
        | tar -xz -C /opt \
    && ln -s /opt/signal-cli-${version}/bin/signal-cli /usr/local/bin/signal-cli

# JSON-RPC over HTTP listens here; account state lives on the PVC at /data.
EXPOSE 8080/tcp
ENV SIGNAL_CLI_CONFIG=/data

# Run the daemon forever. Account registration is done out-of-band via
# `kubectl exec ... -- signal-cli ... register/verify`, after which the
# daemon serves JSON-RPC over HTTP for the registered accounts.
ENTRYPOINT ["signal-cli", "--config", "/data", "daemon", "--http", "0.0.0.0:8080"]
