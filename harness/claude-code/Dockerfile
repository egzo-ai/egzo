# Claude Code as an egzo harness: the real `claude` TUI, started by the egzo session holder.
# Build: docker build --build-arg EGZO_IMAGE=ghcr.io/egzo-ai/egzo:<version> -t egzo-harness-claude-code harness/claude-code
ARG EGZO_IMAGE=ghcr.io/egzo-ai/egzo:latest
# The pinned versions of the harness and of the Playwright MCP server (its Chromium comes with it); bump them deliberately.
ARG PLAYWRIGHT_MCP_VERSION=0.0.83
ARG GIT_VERSION=2.53.0
ARG CLAUDE_CODE_VERSION=2.1.293
FROM ${EGZO_IMAGE} AS egzo

# git >= 2.48: egzo makes worktrees with relative links (`git worktree add --relative-paths`) and sets the
# relativeWorktrees repository extension, which Debian's git (2.39 on bookworm) refuses with "unknown repository
# extension". Built from source so the version does not depend on the distribution.
FROM docker.io/library/node:22-bookworm-slim AS git
ARG GIT_VERSION
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl xz-utils gcc make libc6-dev libssl-dev libcurl4-openssl-dev zlib1g-dev libexpat1-dev \
 && rm -rf /var/lib/apt/lists/* \
 && curl -fsSL https://www.kernel.org/pub/software/scm/git/git-${GIT_VERSION}.tar.xz | tar -xJ -C /tmp \
 && make -C /tmp/git-${GIT_VERSION} -j"$(nproc)" prefix=/usr/local NO_GETTEXT=1 NO_TCLTK=1 NO_PERL=1 NO_PYTHON=1 NO_INSTALL_HARDLINKS=1 install

FROM docker.io/library/node:22-bookworm-slim
ARG CLAUDE_CODE_VERSION
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl procps ripgrep libcurl4 zlib1g libexpat1 \
 && rm -rf /var/lib/apt/lists/*

COPY --from=git /usr/local/ /usr/local/

# The browser every egzo harness image carries: Chromium behind the Playwright MCP server, installed here because
# anything fetched at run time has to pass the egress profile. `playwright-mcp install-browser` is the package's
# own installer (its bundled playwright-core wants another Chromium revision than `npx playwright install`);
# --no-shell skips the headless shell, which the configuration `egzo agent browser` writes never launches. The
# libraries are what Chromium links against, not `install-deps` (which adds a virtual display and a dozen font
# packages); `libnss3-tools` brings `certutil`, which `egzo agent run` uses to put the project CA where Chromium
# reads it. The same Chromium exists for linux/arm64 (the release builds both architectures).
ARG PLAYWRIGHT_MCP_VERSION
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      fonts-liberation fonts-noto-color-emoji libasound2 libatk-bridge2.0-0 libatk1.0-0 libcairo2 libcups2 \
      libdbus-1-3 libdrm2 libexpat1 libgbm1 libglib2.0-0 libnspr4 libnss3 libnss3-tools libpango-1.0-0 libx11-6 \
      libxcb1 libxcomposite1 libxdamage1 libxext6 libxfixes3 libxkbcommon0 libxrandr2 \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g @playwright/mcp@${PLAYWRIGHT_MCP_VERSION} \
 && playwright-mcp install-browser --no-shell --no-progress chrome-for-testing \
 && chmod -R a+rX /opt/ms-playwright \
 && npm cache clean --force

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
