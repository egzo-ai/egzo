# The base of the egzo harness images: Ubuntu with the tools agents reach for (git, Python, ripgrep, jq), Node, Chromium behind the
# Playwright MCP server, and the egzo binary as the entrypoint. A harness image is `FROM` this one and adds its own TUI
# (harness/claude-code, harness/opencode); so does any image of your own that you want to run as a harness (docs/images.md).
# Build: docker build --build-arg EGZO_IMAGE=ghcr.io/egzo-ai/egzo:<version> -t egzo-harness-base harness/base
ARG EGZO_IMAGE=ghcr.io/egzo-ai/egzo:latest
# The pinned versions of Node (its official build, checked against these hashes) and of the Playwright MCP server (its Chromium
# comes with it). scripts/bump-pins.sh proposes new ones; it checks Node's signed checksums before it writes the hashes.
ARG NODE_VERSION=24.22.1
ARG NODE_SHA256_AMD64=6c172a9ab6718f7b801fb5a7bd486af2ee5e32758086a04b84fcce2aa77095e6
ARG NODE_SHA256_ARM64=dbabf7a1cbf947e6059ffad2c27b80041db97dea1e553b62afdee7f13fca336e
ARG PLAYWRIGHT_MCP_VERSION=0.0.83
FROM ${EGZO_IMAGE} AS egzo

# Ubuntu 26.04: git >= 2.48 (egzo makes worktrees with relative links, `git worktree add --relative-paths`, and sets the
# relativeWorktrees repository extension, which an older git refuses), a current Python, and the platform Playwright lists for
# both architectures. Python comes with venv: an agent installs a project's packages into an environment of its own.
FROM docker.io/library/ubuntu:26.04@sha256:6a06111c3b0a8600feadb270aec771dc239bf407b25625f93f44ab00e11f4d3c
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl git jq openssh-client procps python3 python3-venv ripgrep xz-utils \
 && rm -rf /var/lib/apt/lists/*

# Node's official build for this architecture, from nodejs.org, refused unless it is the build the hashes above name.
ARG NODE_VERSION
ARG NODE_SHA256_AMD64
ARG NODE_SHA256_ARM64
RUN case "$(dpkg --print-architecture)" in \
      amd64) arch=x64 sum=$NODE_SHA256_AMD64 ;; \
      arm64) arch=arm64 sum=$NODE_SHA256_ARM64 ;; \
      *) echo "no Node build pinned for $(dpkg --print-architecture)" >&2; exit 1 ;; \
    esac \
 && curl -fsSL -o /tmp/node.tar.xz "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${arch}.tar.xz" \
 && echo "$sum  /tmp/node.tar.xz" | sha256sum -c - \
 && tar -xJf /tmp/node.tar.xz -C /usr/local --strip-components=1 --no-same-owner \
      --exclude CHANGELOG.md --exclude LICENSE --exclude README.md --exclude include --exclude share/doc \
 && rm /tmp/node.tar.xz \
 && node --version && npm --version

# The browser every egzo harness image carries: Chromium behind the Playwright MCP server, installed here because
# anything fetched at run time has to pass the egress profile. `playwright-mcp install-browser` is the package's
# own installer (its bundled playwright-core wants another Chromium revision than `npx playwright install`);
# --no-shell skips the headless shell, which the configuration `egzo agent browser` writes never launches. The
# libraries are what Chromium links against on Ubuntu 26.04 (Playwright's own list for it, the same on both architectures), not
# `install-deps` (which adds a virtual display and a dozen font packages); `libnss3-tools` brings `certutil`, which
# `egzo agent run` uses to put the project CA where Chromium reads it.
ARG PLAYWRIGHT_MCP_VERSION
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      fonts-liberation fonts-noto-color-emoji libasound2t64 libatk-bridge2.0-0t64 libatk1.0-0t64 libatspi2.0-0t64 libcairo2 \
      libcups2t64 libdbus-1-3 libdrm2 libgbm1 libglib2.0-0t64 libnspr4 libnss3 libnss3-tools libpango-1.0-0 libx11-6 libxcb1 \
      libxcomposite1 libxdamage1 libxext6 libxfixes3 libxkbcommon0 libxrandr2 \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g @playwright/mcp@${PLAYWRIGHT_MCP_VERSION} \
 && playwright-mcp install-browser --no-shell --no-progress chrome-for-testing \
 && chmod -R a+rX /opt/ms-playwright \
 && npm cache clean --force

# The agent runs as whatever user egzo gives it (the invoking user), so its home and the workspace
# root are writable by anyone; the home is a volume, so conversations survive recreating the agent.
ENV HOME=/home/agent
RUN mkdir -p /home/agent /workspace && chmod 0777 /home/agent /workspace

COPY --from=egzo /egzo /usr/local/bin/egzo
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/egzo", "agent", "run", "--"]
