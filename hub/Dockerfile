# The hub image. The build context is the repository root (docker build -f hub/Dockerfile .): the hub module requires core
# from ../ (see roadmap/hub.md).
ARG GO_VERSION=1.27.3
# BuildKit sets it; the legacy builder needs it passed (the Makefile does).
ARG BUILDPLATFORM

# The web build and the Go build run on the builder's own architecture: the web output is the same everywhere and Go
# cross-compiles, so an arm64 image is not built under emulation.
FROM --platform=$BUILDPLATFORM docker.io/library/node:22-alpine@sha256:fe4f6d74e20370acb04ad889077d14d0f64a9c8ea6a42bc60cddf1e844699914 AS web
WORKDIR /src/hub/web
COPY hub/web/package.json hub/web/package-lock.json hub/web/.npmrc ./
RUN npm ci --no-audit --no-fund
COPY hub/web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM docker.io/library/golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY hub/go.mod hub/go.sum hub/
RUN cd hub && go mod download
COPY . .
COPY --from=web /src/hub/internal/web/dist hub/internal/web/dist
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN cd hub && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-X github.com/egzo-ai/egzo/internal/version.Version=${VERSION}" -o /egzo-hub ./cmd/egzo-hub

FROM docker.io/library/alpine:3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 hub && mkdir /data && chown hub /data
COPY --from=build /egzo-hub /egzo-hub
USER hub
VOLUME /data
EXPOSE 8080 8081
ENTRYPOINT ["/egzo-hub"]
