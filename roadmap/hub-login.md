# Roadmap: hub login, without Dex and with a starter account

**Status.** Proposal. Nothing is built.

Today the hub can only log people in through OpenID Connect, and the compose file bundles Dex to make that possible. The cost
is a second public port and hostname (the browser has to reach Dex itself), an `/etc/hosts` trick for a first try, an `issuer`
and `redirectURIs` in `dex.yaml` to keep in step with the hub's address, and no way in at all until an identity provider
runs. Comparable tools (Portainer, Arcane, Dockge) ship one container, one port and a built-in login; none bundles an identity
provider.

## The change

1. **Drop Dex from the hub's compose file.** The hub does not run an identity provider and does not document how to run one.
   Whoever wants SSO brings their own. Dex stays only where the tests need a real provider (`make hub-stack`), as a test fixture
   under `hub/` and not as part of the deployment the docs describe.
2. **Keep OIDC, made generic.** The `EGZO_OIDC_*` variables stay. OIDC becomes optional: with no issuer set, only the starter
   account exists. The code follows what every mainstream provider (Keycloak, Authentik, Authelia, Zitadel, Okta, Entra ID,
   Google, GitLab, Auth0, Dex) supports, and no more. See "OIDC".
3. **A starter account with a built-in login page.** One username-and-password account, for starting out and demos. Any setup
   with more than one person connects an identity provider.
4. **A model where a person and the ways they sign in are separate.** Users, passwords and provider identities are separate
   tables, and nothing is ever matched or merged by email. See "The model".

One container, one port, one volume. The compose file loses the `dex` service, its volume and its port, and `.env` loses
`EGZO_OIDC_CLIENT_SECRET` as a required value.

## The model

A person has an egzo-side identity and any number of ways to prove it. The provider's data about them is kept apart from egzo's.

```
users            id, username (unique, case-insensitive), is_operator, disabled_at (null = enabled), created_at, last_login_at
user_passwords   user_id (primary key), hash, updated_at
user_identities  id, user_id, issuer, subject, unique (issuer, subject),
                 email, email_verified, name, preferred_username, picture_url,
                 created_at, last_login_at
sessions         token_hash, user_id, identity_id (null for a password sign-in), expires_at
```

- **`users` is the person as egzo knows them.** The username is chosen by egzo (see "Usernames"), is the login name of a password
  account, and is what the UI calls the person. No provider ever writes to it. The row holds no secret and no provider data.
- **A password is a row of its own** in `user_passwords`, so a user query cannot leak a hash. More local accounts later are more
  rows and a way to make them; the schema does not change.
- **An identity is one account at one provider**, found by (issuer, subject): the only stable identifier OpenID Connect gives.
  The provider's claims (email, name, username, avatar) are a snapshot kept on the identity, refreshed at every login and
  display-only. A user with two identities could have two emails and both are right.
- **A session knows how it was made.** Removing an identity removes the sessions made with it.
- **Nothing is matched by email.** Not to find a user, not to link, not to promote. Other tools learned why: Grafana turned email
  lookup off after [CVE-2023-3128](https://git.almalinux.org/rpms/grafana/commit/f70a7074348ebce8503cd63eca3b9fcc635611b7) (an
  Entra ID takeover) and now documents it as insecure; GitLab and Open WebUI ship email merging off with a warning; Gitea warns
  against its automatic mode. `email_verified` gates nothing.
- **A user is never deleted, only disabled.** The audit log and the agents' `actor` labels refer to a person as `user:<id>` and are
  turned into a name when shown. With the row gone every one of them reads "a removed user". See "Disabling users".

### Usernames

A username is a handle, unique across the hub, that people can read when granting access. It is generated once, when the user is
made, and never changes (renaming is a later feature) and never follows the provider.

1. **Candidates, in order:** the provider's `preferred_username`, the local part of the email, the `name`, and finally `user`. A
   candidate that contains `@` is cut at it (Entra ID's `preferred_username` is `alex@corp.com`). The domain is never used.
2. **Normalize:** Unicode NFKD, drop the accents, lowercase, every run of characters outside `a-z 0-9 . _ -` becomes one `-`,
   leading and trailing `.`, `_` and `-` are cut, 32 characters at most. A username matches `^[a-z0-9][a-z0-9._-]{0,31}$`.
3. **The first candidate that is not empty after normalizing is used** (a name written only in another script normalizes to
   nothing, so the next candidate gets its turn).
4. **If it is taken** (case-insensitive) **or reserved**, the smallest free `-2`, `-3`, ... is appended, shortening the base to fit.
   The reserved names are `admin` (the starter account) and `operator` and `hub` (actors the hub already shows by those names).
   Two logins at once are settled by the unique index and a retry.
5. **A name is never freed:** users are not deleted, and the unique index covers disabled rows too.

The starter account's username is `admin`, set directly and not generated.

## The starter account

- **Variables.** `EGZO_ADMIN_PASSWORD` and `EGZO_LOCAL_LOGIN`. The password variable follows Grafana (`GF_SECURITY_ADMIN_PASSWORD`)
  and Portainer; the username is always `admin`.
- **At start**, if `EGZO_LOCAL_LOGIN` is true (the default), the hub makes sure the user `admin` exists, is enabled and is an
  operator (it creates or repairs it as needed), then settles its password:
  - `EGZO_ADMIN_PASSWORD` **set**: it is saved as the password, on every start. The environment is the source of truth.
  - `EGZO_ADMIN_PASSWORD` **unset**, and `admin` has **no** password: the hub generates one (24 random characters) and prints it
    **once** on standard output, in a clearly marked block.
  - `EGZO_ADMIN_PASSWORD` **unset**, and `admin` **has** a password: left as it is. Nothing is printed.
- **It is for starting out and demos.** The hub has no page to change a password, to make local users or to reset one. The
  password is whatever the environment says, or the generated one.
- **`EGZO_LOCAL_LOGIN=false`** removes the password form and `POST /auth/login`, and makes nothing: an `admin` that already exists
  keeps its row and its identities, and signs in through them. It needs OIDC configured: with `false` and no issuer the hub
  refuses to start. `EGZO_ADMIN_PASSWORD` set together with `false` is ignored, with a warning in the log.
- **No operator, no way in.** A fresh database with `EGZO_LOCAL_LOGIN=false` has no operator. The hub logs an error that says to
  start with `EGZO_LOCAL_LOGIN=true`, link SSO and turn it off again, and refuses to serve the login. This **replaces "the first
  person to sign in is an operator"**, which with an open OIDC provider means "whoever reaches the hub first".
- **A lockout warning.** With `EGZO_LOCAL_LOGIN=false`, if no enabled operator has an identity at the configured issuer, nobody
  can sign in as an operator. The hub starts and says so in the log, naming the issuer and the fix (the break-glass below).
- **Break-glass** is the same two variables: set `EGZO_LOCAL_LOGIN=true` and `EGZO_ADMIN_PASSWORD` and restart. It works when the
  password is lost or the provider is gone, with no subcommand and no editing of the database. This closes the gap in
  [hub-settings.md](hub-settings.md). The password has to be set: with it unset and an old one in place, nothing is printed.
- **Hashing.** argon2id with the library's recommended parameters, encoded with them so they can be raised later.
- **The login page** is a page of the SPA over `POST /auth/login` (username, password), with the SSO button below it when OIDC is
  configured (`EGZO_OIDC_NAME`, default "SSO"). Same session cookie as the OIDC path, same lifetime. A failure says "wrong
  username or password" whether the username exists, the password is wrong or the user is disabled, and costs the same time in
  every case.
- **Brute force.** A per-account and per-address delay that grows after failures (no permanent lockout, which would let anyone
  lock the operator out). Failures go to the audit log.
- **Cookies and CSRF** as the OIDC path already does: `SameSite=Lax`, `Secure` when the public URL is https.

## Signing in with SSO

- The hub looks the identity up by (issuer, subject), with the configured issuer only: an identity of another issuer never matches.
  - **Found, user enabled:** the user signs in, and the identity's snapshot and `last_login_at` are refreshed.
  - **Found, user disabled:** refused with a plain "sign-in refused" and no reason. Nothing is refreshed, no session is made, and
    **no new user is made**. The refusal is written to the audit log, for the operators.
  - **Not found:** a new user is made (username as above) with a new identity and **no access**.
- **Granting a teammate access.** They sign in once and see nothing, then an owner picks them from the candidates in a project's
  Members tab, as today. There are no pre-grants and no invitations.
- **Imported** from the `openid profile email` scopes, in the id_token and falling back to the userinfo endpoint: `email`,
  `email_verified`, `name`, `preferred_username` and `picture`. Groups, roles and anything else are not imported. Any of them may
  be missing; the username falls back as described above.
- **The people picker, the Members tab and Settings, Users** show the username, then the provider's name, email and avatar (from
  the identity signed in most recently), how the person signs in (password, or the provider), and the last sign-in. Those separate
  two "Alex"es, and a stale user from a former provider from the live one.
- **The avatar is fetched by the browser, never by the hub.** A fetch by the hub from a URL the provider supplies (which a user may
  control) is a server-side request forgery against whatever the hub can reach: engines, cloud metadata. The cost is that the hub's
  content security policy widens from `img-src 'self' data:` to `img-src 'self' data: https:`. An injected `<img>` could then send
  data to any https host, which matters only if markup injection ever appears (the app escapes text and the terminal is drawn by
  xterm). The image is requested with `referrerpolicy="no-referrer"`, only `https:` URLs are kept, and the initials show when it
  cannot load. The docs say that avatar hosts see who views the page.

## Linking an identity provider

The way from the starter account to SSO, and from one provider to another, for anyone who can sign in: **Link `<SSO name>`** in the
user's own menu.

1. The signed-in user starts the link (a `POST`, so a page cannot start it for them). The hub records in the OIDC state which user
   it is for.
2. They sign in at the provider. The callback checks that the signed-in user is still that user, **adds the identity to them**, and
   creates no new session.
3. If the (issuer, subject) already belongs to **another user**, and that user is **enabled**, the hub refuses and names them
   (moving the identity would take their access away). If that user is **disabled** (an operator disables an accidental row for
   this, made by signing in with SSO before linking), the identity moves to the user linking: they have just proved control of it
   at the provider. The old row stays disabled, its grants dormant. If it already belongs to this user, nothing changes.
4. The menu then shows "Linked as `<email>` at `<issuer>`" and an **Unlink**. A way to sign in is a password (while
   `EGZO_LOCAL_LOGIN` is true) or an identity at the configured issuer. Unlinking is refused when it would remove the last one;
   unlinking an identity of a former issuer never does. The sessions made with the identity end.
5. Link, move and unlink are written to the audit log.

So the move to SSO is: sign in as `admin`, click Link, sign in at the provider, sign out and in again with SSO to check, set
`EGZO_LOCAL_LOGIN=false`. The operator and everything they own is the same user throughout.

## Changing the identity provider

The hub trusts the one issuer it is configured with. What to expect when it changes (to document, in this order):

- **Identities from the old issuer cannot sign in.** They stay in the database, listed under the user as "`<issuer>` (not the
  current provider)", and the sessions made with them are ended when the hub starts with the new issuer. The log says so, naming
  both issuers.
- **Everyone who signs in at the new provider is a new user with no access,** even with the same email, since issuer and subject
  differ. Their username is generated again, and gets a `-2` because the old user's name stays taken. Their old user's roles do
  not follow. Nothing is merged, so nothing can be merged wrongly.
- **A user who can still sign in with a password moves their own account:** Link the new provider (this adds an identity), then
  Unlink the old one. Their roles, ownership and agents are untouched, since they belong to the user. In practice this is the
  starter account. If `EGZO_LOCAL_LOGIN=false`, set it to `true` and restart first.
- **Everyone else is granted again** by the owners of each project, after they sign in once. That includes other operators, who
  have no password: `admin` promotes them again under Settings, Users. Then an operator disables the old users. Their agents,
  and the audit log, still show their old username.
- **The suggested order:** turn local login on and set the admin password, change the issuer and restart, sign in as `admin`, link
  the new provider and unlink the old, have the team sign in, grant them (and promote the other operators), disable the old users,
  turn local login off.
- **Entra ID** gives each app registration its own subject: re-registering the app has the same effect as changing provider.
- Not built: an operator action "move this user's roles to that user". If a team finds the re-granting too much, that is the next
  step, not email matching.

## Disabling users

`Store.DeleteUser` exists and nothing calls it (see [hub-settings.md](hub-settings.md)). It goes: the hub has no hard delete of a
person, for the reason in "The model". Settings, Users gets **Disable** and **Enable** for an operator, with a confirmation:

- **Disabled** means `disabled_at` is set (the time says when; the audit entry says by whom, and its detail names the user). The
  user's sessions end and their open terminals are closed. Sign-in is refused by password and by SSO alike, in the plain wording
  of any other failure, with no hint that the account exists or is disabled.
- **The identities stay attached**, so the same person signing in again meets the disabled account and does not become a second,
  new user. Their grants stay too and are dormant; enabling the user brings them back. The Members tab shows a disabled member
  as such, and the people picker leaves them out. The one thing that takes an identity from a disabled user is a link by someone
  who proves control of it (see "Linking").
- **An operator cannot be disabled**, so not yourself and never the last one: demote first, as the store enforces for deletion
  today. The "no operator" start-up check counts enabled operators only.
- **Their agents keep running.** Disabling is about signing in; an owner or administrator stops or removes agents as usual.
- **Settings, Users hides disabled users** behind a filter, so the rows left by a provider switch do not clutter the list.
- Not built: erasing a person. A self-hosted hub of this size can do it with the database if it must.

## OIDC

What the code does today is already standard: discovery, authorization code flow, `state` and `nonce`, id_token verified against
the provider's keys, identity is issuer plus `sub`. To be generic enough without testing every provider:

- **PKCE (S256)** on every authorization request, as OAuth 2.1 and every current provider recommend. Providers that do not know it
  ignore the parameter.
- **Client authentication** by whatever the provider's discovery document lists (`client_secret_basic`, then `_post`); today it is
  the library's guess. A public client (no secret) with PKCE is allowed: `EGZO_OIDC_CLIENT_SECRET` becomes optional.
- **Scopes.** `openid profile email` by default, `EGZO_OIDC_SCOPES` to replace them (some providers want `offline_access`, some
  reject scopes they do not know).
- **Claims.** As in "Signing in with SSO". The userinfo fallback covers providers that leave claims out of the id_token (Entra ID
  and Okta can).
- **Issuer matching** is exact, as the spec says. The error for a mismatch names both strings, so a trailing slash or `http` against
  `https` is visible at once.
- **Redirect address** is `<public URL>/auth/callback`. The login page and the logs show the exact value, so it can be pasted into
  the provider's client registration.
- **Private CA.** Go reads `SSL_CERT_FILE` and `SSL_CERT_DIR`; the docs say so, with a compose example. No hub-specific variable.
- **Reachability.** The hub (server side) and the browser both need the issuer. Documented as the first thing to check.
- **Not in scope.** Groups or roles from claims (roles stay in the hub), RP-initiated logout (signing out ends the hub session
  only), back-channel logout, more than one issuer at a time (the schema allows it), SAML.

## Documentation

`hub/docs/README.md` is rewritten around this:

- **Quick start**: `docker compose up`, set `EGZO_SECRET_KEY`, read the `admin` password in `docker compose logs hub`.
- **Putting it on the internet**: one port, a TLS reverse proxy in front (a Caddy and an nginx snippet), `EGZO_PUBLIC_URL` and
  `EGZO_TRUSTED_PROXIES`.
- **Single sign-on**: the client to register (confidential or public with PKCE, authorization code, the redirect address, the
  scopes), the variables, linking from the starter account, and a table of the failures (redirect mismatch, issuer mismatch,
  unreachable issuer, unknown CA) with the fix for each. One worked example each for Keycloak, Authentik and Google; others
  follow the same shape.
- **Giving a teammate access**: they sign in once, then an owner grants a role in the Members tab.
- **Changing the identity provider**: the section above.
- **The avatar**: fetched by the browser; the hub's CSP allows https images; avatar hosts see viewers.
- The `/etc/hosts` line, the Dex section and the `dex.yaml` comments go.

## Steps

Each is a commit with Go unit tests (the hub is not specs-first), and each leaves the hub working.

1. **The schema and the store**: `users`, `user_passwords`, `user_identities`, `sessions` (with the cascade from an identity to
   its sessions), setting and clearing `disabled_at`; username generation and its tests; the first-sign-in-is-operator rule (and
   its audit entry) removed. The hub records a schema version and, on an older database, refuses to start and says to remove
   `hub.db` (pre-1.0 has no migrations).
2. **The starter account and the login page**: `EGZO_ADMIN_PASSWORD`, `EGZO_LOCAL_LOGIN`, argon2id, `/auth/login`, throttling,
   the SPA page, the start-up checks and the lockout warning. Store and web tests; a Playwright test of the form.
3. **SSO sign-in on the new model**: identity lookup, new users, the imported fields and the userinfo fallback. Tests include: an
   identity of another issuer never matches; **a disabled user who signs in with SSO is refused and no new user row is made**
   (count the rows before and after), with nothing refreshed and no session; the same for a password sign-in.
4. **Link and unlink**: the OIDC state, the refusals, the move from a disabled user, the audit entries, sessions ended on unlink,
   and ended at start for identities of another issuer.
5. **Disable and enable a user**: `PUT /users/{id}` `{disabled}`, the buttons and their confirmation, the filter, the audit entry,
   sessions and terminals ended. `Store.DeleteUser` is removed.
6. **OIDC generic**: PKCE, client authentication, optional secret, scopes, clear errors. Tests against `oidctest` with providers
   that behave differently (claims missing from the id_token, no `email_verified`, public client).
7. **People in the UI**: username, avatar, sign-in method and last sign-in in the picker, the Members tab and Settings, Users;
   the CSP change. Vitest and a Playwright test.
8. **Compose and docs**: drop Dex from `compose.yaml`, `.env.example` and the docs; keep a Dex fixture for `hub-stack`, which then
   exercises the OIDC path and the link flow with the starter account present.

No compatibility is kept: the first-user rule, the old `users` columns and the bundled Dex are removed outright.

## Open questions

- **Renaming a user.** Generated usernames can be unlovely (`alex-2`). An operator rename is a small action to add later; the
  starter account's `admin` is looked up by name, so it would need to stay fixed.
- **Printing a secret to the log** is the convention (Jenkins, Pi-hole) and the quick start relies on it, but logs get shipped.
  The mitigation is printing only when there is no password yet. Whoever wants no secret in the log sets `EGZO_ADMIN_PASSWORD`, or
  connects an identity provider and sets `EGZO_LOCAL_LOGIN=false`.
