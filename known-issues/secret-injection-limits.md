# Secret injection does not work for every client or protocol

Status: **accepted** (written 2026-10-10).

egzo keeps secrets out of agents by rewriting HTTPS requests in the proxy: `inject` sets a header, a `placeholder`
is swapped for the secret (`DESIGN.md`, "Secrets and vaults"). That covers an API key in a header and a password in
a form. It does not cover everything a client can do with a secret, and in each case below the agent is not given
the secret to work around it: the feature does not work, and says nothing is injected.

## Signed requests (HMAC, AWS SigV4, signed URLs, JWTs the client mints)

A client that signs a request uses the secret to compute the signature, and the signature covers the method, the
path, the headers or the body. The agent only has a placeholder, so it signs with that, and the server rejects the
request. The proxy replaces text; it does not know the signing scheme, so it cannot sign again. Anything that needs
the *real key in the client* is out of reach: AWS SDKs and CLI, Stripe-style webhook signing, OAuth 1.0, and the like.

Works instead: services where the credential is the header (bearer tokens, API keys, session cookies). For a signed
API, give the agent a credential that does not sign (a short-lived token minted outside the sandbox), or run the
signing step outside it.

## Credentials the client transforms before sending

The proxy matches the placeholder text, so it must reach the wire unchanged. A client that encodes or hashes it first
hides it:

- **HTTP Basic authentication** is `base64(user:password)`. A client that builds it from the placeholder sends the
  base64 of the placeholder. Use `inject` with a `value` for the whole header instead.
- A login page whose JavaScript hashes or encrypts the password in the browser (a client-side `sha256`, SRP,
  password-manager style derivation) sends the hash of the placeholder.
- Any format that transforms the text: URL-safe base64, gzip of the body (`Content-Encoding` is not scanned),
  encryption inside the payload.

## Clients that do not behave like a proxy-aware HTTPS client

The proxy only sees what is sent to it as a `CONNECT` tunnel to port 443, and can only rewrite what it terminates
with the project CA. These do not get injection, or do not work at all:

- Clients that ignore `HTTPS_PROXY` (some language runtimes and SDKs, and software that opens sockets itself). The
  agent's network has no other route, so they fail to connect rather than leak.
- Clients that do not read the CA bundle egzo provides. Chromium uses its own NSS database, not `SSL_CERT_FILE`
  (`roadmap/browser-access.md`); Java uses its own keystore; some Python HTTP libraries bundle their own CA list.
  They reject the proxy's certificate for any host that is injected into or inspected.
- Clients that pin certificates, for the same reason.
- Anything that is not HTTPS on port 443: SSH, raw TCP, plain HTTP (refused), UDP, a database protocol. A git remote
  over SSH cannot use a secret from egzo; git over HTTPS can.
- WebSocket messages after the upgrade are not rewritten.

## Request bodies to a placeholder host

The proxy reads a request body to find the placeholder, so it holds it in memory: up to 1 MiB, uncompressed. A request
to a placeholder host that does not fit those limits is **refused**, not forwarded: `413` for a body over 1 MiB and
`415` for a compressed one (`Content-Encoding`), with a message naming the reason, and a `deny` in the audit log.

This is deliberate. Forwarding the request with the placeholder still in it would make some requests work and others
fail with nothing to tell them apart, which is worse than a clear refusal. The rule depends only on the request, so
the same request always has the same outcome. The cost is that a host with a placeholder cannot take big uploads: use
`inject` for such a host, or a service for the upload that has no placeholder. Raising the limit, or scanning the
stream instead of buffering it (a fixed-length placeholder can be found in a stream, but then the request has to be
sent chunked, which some servers refuse), is possible if a real use-case needs it.

## What the agent sees when it fails

Nothing is retried with the real secret, and nothing falls back to sending it. The server gets the placeholder (or no
header), and the request fails the way a wrong password fails. A body the proxy cannot scan is refused with a message
instead (see above). `egzo proxy log` shows the request on an intercepted
host; it never shows a body.

## Revisit

If a real use-case needs one of these, the likely fixes are on the proxy side (re-signing for a named scheme, a
`placeholder` form for Basic authentication), added for that scheme only, with specs. Per-client problems with the CA
are image problems and belong in the image (`docs/images.md`).
