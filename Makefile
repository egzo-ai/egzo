# The sidecar image runs the binary in a scratch container, so it must be static.
GO ?= go
BIN := bin/egzo

.PHONY: build test lint specs clean image images hub-web hub-web-test hub-build hub-test hub-lint hub-image hub-e2e hub-stack

VERSION ?= dev
REGISTRY ?= ghcr.io/egzo-ai
# The Dockerfiles build on $BUILDPLATFORM, which only BuildKit sets; passing it keeps `docker build` working without buildx.
BUILD_PLATFORM := --build-arg BUILDPLATFORM=linux/$(shell $(GO) env GOARCH)

# The sidecar image, the harness base image built on it, and the harness images built on that. VERSION is also what the CLI looks for.
image:
	docker build $(BUILD_PLATFORM) --build-arg VERSION=$(VERSION) -t $(REGISTRY)/egzo:$(VERSION) .

images: image
	docker build --build-arg EGZO_IMAGE=$(REGISTRY)/egzo:$(VERSION) -t $(REGISTRY)/egzo-harness-base:$(VERSION) harness/base
	for harness in claude-code opencode codex; do \
		docker build --build-arg BASE_IMAGE=$(REGISTRY)/egzo-harness-base:$(VERSION) -t $(REGISTRY)/egzo-harness-$$harness:$(VERSION) harness/$$harness || exit 1; \
	done

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN) ./cmd/egzo

test:
	$(GO) vet ./...
	$(GO) test -race ./...

lint:
	scripts/check-core-independent.sh
	staticcheck ./...
	scripts/govulncheck.sh

# ENGINE is one of: docker, docker-gvisor, podman, podman-rootless; empty chooses from this machine
ENGINE ?=

specs: build
	cd specs && EGZO_BIN=$(CURDIR)/$(BIN) .venv/bin/pytest $(if $(ENGINE),--engine $(ENGINE))

clean:
	rm -rf bin

# --- the hub (hub/ is its own Go module; see roadmap/hub.md) ---------------------------------------------------------------------

# The web app (Svelte, TypeScript, Vite; hub/web) built into hub/internal/web/dist, which the hub embeds. Needs node, or docker.
hub-web:
	if command -v npm >/dev/null; then cd hub/web && npm ci --no-audit --no-fund && npm run build; \
	else docker run --rm -u $$(id -u):$$(id -g) -e HOME=/tmp -v $(CURDIR)/hub:/w -w /w/web docker.io/library/node:22-alpine sh -c "npm ci --no-audit --no-fund && npm run build"; fi

# The web app's type check and unit tests (after hub-web, which installs its packages).
hub-web-test:
	if command -v npm >/dev/null; then cd hub/web && npm run check && npm test; \
	else docker run --rm -u $$(id -u):$$(id -g) -e HOME=/tmp -v $(CURDIR)/hub:/w -w /w/web docker.io/library/node:22-alpine sh -c "npm run check && npm test"; fi

hub-build: hub-web
	cd hub && CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-X github.com/egzo-ai/egzo/internal/version.Version=$(VERSION)" -o ../bin/egzo-hub ./cmd/egzo-hub

hub-test:
	cd hub && $(GO) vet ./... && $(GO) test -race ./...

hub-lint:
	cd hub && staticcheck ./... && ../scripts/govulncheck.sh

# The hub image: the context is the repository, because the hub module requires core from ../
hub-image:
	docker build $(BUILD_PLATFORM) -f hub/Dockerfile --build-arg VERSION=$(VERSION) -t $(REGISTRY)/egzo-hub:$(VERSION) .

# The browser tests, in Playwright's image, against the hub with fake engines and a test login provider.
hub-e2e: hub-web
	cd hub && CGO_ENABLED=0 $(GO) build -o e2e/server/egzo-hub-e2e ./e2e/server
	docker build -q -t egzo-hub-e2e hub/e2e
	docker run --rm --network host --ipc=host -v $(CURDIR)/hub:/hub egzo-hub-e2e pytest -q -p no:cacheprovider

# The whole stack for real: hub image + Dex from compose.yaml, the local engine, a project made by the CLI, a browser logging in through Dex.
hub-stack: build hub-image
	hub/e2e/stack.sh
