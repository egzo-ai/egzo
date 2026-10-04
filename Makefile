# The sidecar image runs the binary in a scratch container, so it must be static.
GO ?= go
BIN := bin/egzo

.PHONY: build test specs clean image images

VERSION ?= dev
REGISTRY ?= ghcr.io/egzo-ai

# The sidecar image, and the harness images built on it. VERSION is also what the CLI looks for.
image:
	docker build --build-arg VERSION=$(VERSION) -t $(REGISTRY)/egzo:$(VERSION) .

images: image
	for harness in claude-code opencode; do \
		docker build --build-arg EGZO_IMAGE=$(REGISTRY)/egzo:$(VERSION) -t $(REGISTRY)/egzo-harness-$$harness:$(VERSION) harness/$$harness || exit 1; \
	done

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN) ./cmd/egzo

test:
	$(GO) vet ./...
	$(GO) test -race ./...

# ENGINE is one of: docker, docker-gvisor, podman, podman-rootless
ENGINE ?= docker

specs: build
	cd specs && EGZO_BIN=$(CURDIR)/$(BIN) .venv/bin/pytest --engine $(ENGINE)

clean:
	rm -rf bin
