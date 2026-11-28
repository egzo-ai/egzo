# OpenCode as an egzo harness: the real `opencode` TUI, started by the egzo session holder.
# Build: docker build --build-arg BASE_IMAGE=ghcr.io/egzo-ai/egzo-harness-base:<version> -t egzo-harness-opencode harness/opencode
# The base image (harness/base) has git, the browser and the egzo entrypoint; this one adds the TUI.
ARG BASE_IMAGE=ghcr.io/egzo-ai/egzo-harness-base:latest
ARG OPENCODE_VERSION=2.3.2
# AHP mode (`egzo agent spawn --ahp`): egzo's fork of the opencode-ahp plugin, pinned to a commit. It lands in
# /usr/local/lib/node_modules/opencode-ahp, where the harness looks for it (harness.OpenCodeAHPPlugin); an image without it has no AHP mode.
ARG OPENCODE_AHP_COMMIT=a45765bbf9eaf4fc316772a4513ee1aae92e71de
FROM ${BASE_IMAGE}
ARG OPENCODE_VERSION
ARG OPENCODE_AHP_COMMIT

RUN npm install -g @opencode/cli@${OPENCODE_VERSION} \
 && npm install -g https://codeload.github.com/egzo-ai/opencode-ahp/tar.gz/${OPENCODE_AHP_COMMIT} \
 && test -f /usr/local/lib/node_modules/opencode-ahp/package.json \
 && npm cache clean --force

ENV EGZO_HARNESS=opencode \
    OPENCODE_DISABLE_AUTOUPDATE=1
CMD ["opencode"]
