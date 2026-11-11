# A harness image with Docker inside: any egzo harness image (BASE_IMAGE) plus Docker's engine, sudo, and an entrypoint that starts the
# daemon before the harness when the agent may. It may when its template keeps capabilities (`capabilities: [ALL]`), which also lifts
# no-new-privileges so that sudo works; on an engine whose runtime is sysbox, the daemon then runs in the agent's own sandbox, with no
# host socket and no --privileged (docs/sysbox.md). Without capabilities, sudo cannot gain anything, the daemon is not started, and the
# image runs the harness like the one it is built on.
# Build: docker build --build-arg BASE_IMAGE=ghcr.io/egzo-ai/egzo-harness-claude-code:<version> -t egzo-harness-claude-code-docker harness/docker
ARG BASE_IMAGE=ghcr.io/egzo-ai/egzo-harness-claude-code:latest
FROM ${BASE_IMAGE}

# Ubuntu's Docker: the engine (containerd, runc and iptables come with it), buildx and compose.
RUN apt-get update \
 && apt-get install -y --no-install-recommends docker.io docker-buildx docker-compose-v2 sudo \
 && rm -rf /var/lib/apt/lists/*

COPY sudoers /etc/sudoers.d/egzo-docker
COPY start-docker /usr/local/bin/egzo-start-docker
RUN chmod 0440 /etc/sudoers.d/egzo-docker && chmod 0755 /usr/local/bin/egzo-start-docker && visudo -cf /etc/sudoers.d/egzo-docker

# A new ENTRYPOINT clears the CMD of the image below, so the harness is started with no program and runs its own default
# (claude, opencode or codex), as egzo expects of a harness image.
ENTRYPOINT ["/usr/local/bin/egzo-start-docker"]
