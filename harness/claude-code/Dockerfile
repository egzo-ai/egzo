# Claude Code as an egzo harness: the real `claude` TUI, started by the egzo session holder.
# Build: docker build --build-arg EGZO_IMAGE=ghcr.io/egzo-ai/egzo:<version> -t egzo-harness-claude-code harness/claude-code
ARG EGZO_IMAGE=ghcr.io/egzo-ai/egzo:latest
# The pinned version of the harness; bump it deliberately.
ARG CLAUDE_CODE_VERSION=2.1.293
FROM ${EGZO_IMAGE} AS egzo

FROM docker.io/library/node:22-bookworm-slim
ARG CLAUDE_CODE_VERSION
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git procps ripgrep \
 && rm -rf /var/lib/apt/lists/*
RUN npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} && npm cache clean --force

# The agent runs as whatever user egzo gives it (the invoking user), so its home and the workspace
# root are writable by anyone; the home is a volume, so conversations survive recreating the agent.
ENV HOME=/home/agent \
    EGZO_HARNESS=claude-code \
    DISABLE_AUTOUPDATER=1 \
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
RUN mkdir -p /home/agent /workspace && chmod 0777 /home/agent /workspace

COPY --from=egzo /egzo /usr/local/bin/egzo
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/egzo", "agent", "run", "--"]
CMD ["claude"]
