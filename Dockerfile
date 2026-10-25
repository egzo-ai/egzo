# The all-in-one egzo image: the static egzo binary, which runs every sidecar role (egzo control,
# egzo proxy serve, egzo prep), plus CA roots for the proxy and git (>= 2.48, for relative worktree links) for the prep role.
# The harness images copy the binary out of this image (see harness/*/Dockerfile).
ARG GO_VERSION=1.27.3
# BuildKit sets it; the legacy builder needs it passed (the Makefile does).
ARG BUILDPLATFORM
# The build runs on the builder's own architecture and cross-compiles, so an arm64 image is not compiled under emulation.
FROM --platform=$BUILDPLATFORM docker.io/library/golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-X github.com/egzo-ai/egzo/internal/version.Version=${VERSION}" -o /egzo ./cmd/egzo

FROM docker.io/library/alpine:3
RUN apk add --no-cache ca-certificates git
COPY --from=build /egzo /egzo
