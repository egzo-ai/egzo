# OpenCode as an egzo harness: the real `opencode` TUI, started by the egzo session holder.
# Build: docker build --build-arg BASE_IMAGE=ghcr.io/egzo-ai/egzo-harness-base:<version> -t egzo-harness-opencode harness/opencode
# The base image (harness/base) has git, the browser and the egzo entrypoint; this one adds the TUI.
ARG BASE_IMAGE=ghcr.io/egzo-ai/egzo-harness-base:latest
ARG OPENCODE_VERSION=2.1.3
FROM ${BASE_IMAGE}
ARG OPENCODE_VERSION

RUN npm install -g @opencode/cli@${OPENCODE_VERSION} && npm cache clean --force

ENV EGZO_HARNESS=opencode \
    OPENCODE_DISABLE_AUTOUPDATE=1
CMD ["opencode"]
