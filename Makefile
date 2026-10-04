# The sidecar image runs the binary in a scratch container, so it must be static.
GO ?= go
BIN := bin/egzo

.PHONY: build test specs clean

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
