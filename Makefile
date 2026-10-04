# The sidecar image runs the binary in a scratch container, so it must be static.
GO ?= go
BIN := bin/egzo

.PHONY: build specs clean

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN) ./cmd/egzo

specs: build
	cd specs && EGZO_BIN=$(CURDIR)/$(BIN) .venv/bin/pytest

clean:
	rm -rf bin
