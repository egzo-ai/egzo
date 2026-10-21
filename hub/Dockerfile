# The hub image. The build context is the repository root (docker build -f hub/Dockerfile .): the hub module requires core
# from ../ (see roadmap/hub.md).
ARG GO_VERSION=1.27.3

FROM docker.io/library/node:22-alpine AS web
WORKDIR /src/hub/web
COPY hub/web/package.json hub/web/package-lock.json hub/web/.npmrc ./
RUN npm ci --no-audit --no-fund
COPY hub/web/ ./
RUN npm run build

FROM docker.io/library/golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY hub/go.mod hub/go.sum hub/
RUN cd hub && go mod download
COPY . .
COPY --from=web /src/hub/internal/web/dist hub/internal/web/dist
ARG VERSION=dev
RUN cd hub && CGO_ENABLED=0 go build -trimpath -ldflags "-X github.com/egzo-ai/egzo/internal/version.Version=${VERSION}" -o /egzo-hub ./cmd/egzo-hub

FROM docker.io/library/alpine:3
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 hub && mkdir /data && chown hub /data
COPY --from=build /egzo-hub /egzo-hub
USER hub
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/egzo-hub"]
