# OpenCode as an egzo harness: the real `opencode` TUI, started by the egzo session holder.
# Build: docker build --build-arg EGZO_IMAGE=ghcr.io/egzo-ai/egzo:<version> -t egzo-harness-opencode harness/opencode
ARG EGZO_IMAGE=ghcr.io/egzo-ai/egzo:latest
FROM ${EGZO_IMAGE} AS egzo

FROM docker.io/library/node:22-bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git procps ripgrep \
 && rm -rf /var/lib/apt/lists/*
RUN npm install -g opencode-ai && npm cache clean --force

ENV HOME=/home/agent \
    EGZO_HARNESS=opencode \
    OPENCODE_DISABLE_AUTOUPDATE=1
RUN mkdir -p /home/agent /workspace && chmod 0777 /home/agent /workspace

COPY --from=egzo /egzo /usr/local/bin/egzo
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/egzo", "agent", "run", "--"]
CMD ["opencode"]
