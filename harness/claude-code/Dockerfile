# Claude Code as an egzo harness: the real `claude` TUI, started by the egzo session holder.
# Build: docker build --build-arg BASE_IMAGE=ghcr.io/egzo-ai/egzo-harness-base:<version> -t egzo-harness-claude-code harness/claude-code
# The base image (harness/base) has git, the browser and the egzo entrypoint; this one adds the TUI.
ARG BASE_IMAGE=ghcr.io/egzo-ai/egzo-harness-base:latest
ARG CLAUDE_CODE_VERSION=2.1.308
FROM ${BASE_IMAGE}
ARG CLAUDE_CODE_VERSION

RUN npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} && npm cache clean --force

ENV EGZO_HARNESS=claude-code \
    DISABLE_AUTOUPDATER=1 \
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
CMD ["claude"]
