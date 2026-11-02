# Caddy with the Cloudflare DNS plugin, for certificates over the ACME DNS-01 challenge (the previews need a wildcard). Caddy
# has no DNS provider built in: its docs build one in with xcaddy, as here. compose.yaml passes the versions, from .env.
ARG CADDY_VERSION
FROM docker.io/library/caddy:${CADDY_VERSION}-builder AS build
ARG CADDY_CLOUDFLARE_VERSION
RUN xcaddy build --with github.com/caddy-dns/cloudflare@${CADDY_CLOUDFLARE_VERSION}

FROM docker.io/library/caddy:${CADDY_VERSION}
COPY --from=build /usr/bin/caddy /usr/bin/caddy
