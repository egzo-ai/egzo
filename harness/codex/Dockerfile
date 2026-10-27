# Codex as an egzo harness: the real `codex` TUI, started by the egzo session holder.
# Build: docker build --build-arg BASE_IMAGE=ghcr.io/egzo-ai/egzo-harness-base:<version> -t egzo-harness-codex harness/codex
# The base image (harness/base) has git, the browser and the egzo entrypoint; this one adds the TUI.
ARG BASE_IMAGE=ghcr.io/egzo-ai/egzo-harness-base:latest
ARG CODEX_VERSION=0.164.0
FROM ${BASE_IMAGE}
ARG CODEX_VERSION

RUN npm install -g @openai/codex@${CODEX_VERSION} && npm cache clean --force

ENV EGZO_HARNESS=codex
CMD ["codex"]
