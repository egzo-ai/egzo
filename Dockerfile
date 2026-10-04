# The all-in-one egzo image: the static egzo binary, which runs every sidecar role (egzo control,
# egzo proxy serve, egzo prep), plus CA roots for the proxy and git (>= 2.47) for the prep role.
# The harness images copy the binary out of this image (see harness/*/Dockerfile).
ARG GO_VERSION=1.27.1
FROM docker.io/library/golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-X github.com/egzo-ai/egzo/internal/version.Version=${VERSION}" -o /egzo ./cmd/egzo

FROM docker.io/library/alpine:3
RUN apk add --no-cache ca-certificates git
COPY --from=build /egzo /egzo
