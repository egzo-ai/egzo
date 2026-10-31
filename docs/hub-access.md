# Hub sign-in and roles

Who can sign in to [the hub](hub.md), and what each person may do there.

## The starter account

`admin` signs in with a password, for starting out and for demos. Any setup with more than one person connects an identity
provider (below). There is no page to change the password or to make other local users.

- With `EGZO_ADMIN_PASSWORD` set, that is the password, saved at every start: the environment is the source of truth. Changing it
  signs out whoever signed in with the old one.
- Unset, the hub makes one on the first start and prints it **once** on standard output. Later starts print nothing.
- At every start the hub makes sure `admin` exists, is enabled and is an operator.
- After a few failed tries in a row the account waits a little longer before each next one (the hub is not meant to face the
  internet, so there is no lockout and no limit per address).

## Single sign-on

The hub speaks OpenID Connect to one provider of yours: Keycloak, Authentik, Authelia, Zitadel, Okta, Entra ID, Google, GitLab,
Auth0, Dex... It does not run one. Register a client at the provider:

- authorization code flow, confidential (with a secret) or public (no secret: the hub always uses PKCE);
- redirect address `<EGZO_PUBLIC_URL>/auth/callback` (the hub logs the exact value at start when `EGZO_PUBLIC_URL` is set);
- scopes `openid profile email`.

Then set `EGZO_OIDC_ISSUER` (exactly as the provider writes it, trailing slash included), `EGZO_OIDC_CLIENT_ID`,
`EGZO_OIDC_CLIENT_SECRET` if any, and `EGZO_OIDC_NAME` for the button. Examples of the issuer:

| Provider | `EGZO_OIDC_ISSUER` |
|---|---|
| Keycloak | `https://keycloak.example.com/realms/<realm>` |
| Authentik | `https://authentik.example.com/application/o/<application slug>/` |
| Google | `https://accounts.google.com` (make the OAuth app "Internal" to keep it to your Workspace) |

**Moving from the starter account to SSO:** sign in as `admin`, open your account (your name at the bottom of the sidebar), click
**Link**, sign in at the provider, sign out and back in with SSO to check, then set `EGZO_LOCAL_LOGIN=false` and restart. The
operator, and everything they own, is the same user throughout.

When it does not work:

| Symptom | Fix |
|---|---|
| The provider says the redirect address is not allowed | register `<EGZO_PUBLIC_URL>/auth/callback` exactly (scheme, host, port) |
| "the login provider's issuer is not EGZO_OIDC_ISSUER" | copy the issuer from `<issuer>/.well-known/openid-configuration`; a trailing slash or http/https counts |
| "cannot reach the login provider" | the hub (server side) and the browser must both reach the issuer: check DNS and the network from the hub's container |
| a certificate error | a private CA: mount it and set `SSL_CERT_FILE` (or `SSL_CERT_DIR`) in the hub's environment |

## People

- A person is made the first time they sign in with SSO, with a **username** the hub picks from what the provider says
  (`preferred_username`, then the email's local part, then the name), with `-2`, `-3`... when it is taken. It never changes after.
  They see nothing until an owner gives them a role: **to give a teammate access**, they sign in once, then an owner picks them in
  the project's Members tab.
- Nothing is matched by email. A person is their account at the provider (its issuer and subject); the email and name are shown,
  never trusted.
- The provider's picture is shown as the avatar. It is fetched by the browser, never by the hub, so the hub's content security
  policy allows https images, and the picture's host sees who views the page.
- **Disabling** (Settings, Users) signs a person out, closes their terminals and refuses their sign-in, by password and by SSO alike.
  Their roles stay, unused, until they are enabled again; their agents keep running. People are never deleted: the audit log and
  the agents' labels keep naming them. An operator cannot be disabled; remove operator first.
- A person who signed in with SSO before linking it to `admin` is a second user: disable that user, use **Remove** on its account
  under Settings, Users, then link again.

## Changing the identity provider

The hub trusts the one issuer it is configured with.

- Accounts of the old provider can no longer sign in, and their sessions end at the next start (the log names both issuers).
- Everyone who signs in at the new provider is a **new user with no access**, even with the same email, with a new username (`-2`
  when the old one is taken).
- Whoever can still sign in with a password (in practice `admin`) moves their own account: Link the new provider, then Unlink the
  old one. Their roles and agents stay.
- Everyone else is granted again by each project's owners, after they sign in once; `admin` makes the other operators operators
  again. Then disable the old users.
- In order: set `EGZO_LOCAL_LOGIN=true` and `EGZO_ADMIN_PASSWORD`, change the issuer and restart, sign in as `admin`, link the new
  provider and unlink the old, have the team sign in, grant them, disable the old users, turn local login off.
- Entra ID gives each app registration its own subjects: registering the app again is the same as changing provider.

## Locked out

`EGZO_LOCAL_LOGIN=true` with `EGZO_ADMIN_PASSWORD` set, and a restart, always gets `admin` back in as an operator, whatever
happened to the provider or the password. With local login off and no operator who has an account at the configured provider,
the hub starts and says so in its log; with local login off and no operator at all, it does not start.

## Roles

A role is held on one project. Roles live in the hub's database; the project file knows nothing about them.

| Role | May |
|---|---|
| user | see the project; spawn agents; start, stop, remove and open the terminal of their own agents |
| administrator | the same on every agent of the project; read the audit log and the logs |
| owner | an administrator who also grants and removes roles. An owner cannot remove or lower themselves |

**Operators** are hub-level, not a role. An operator manages the links and the users, approves the projects that ask to link, and holds
**every project as its owner**, without a grant: a project is theirs as soon as it is approved. That includes other people's agents and
terminals, so make somebody an operator only when you would give them what the linked projects' verbs allow. Whoever approves a project
gets a real owner role on it, which stays if they stop being an operator. Operators appear on every project's
Members tab, shaded and tagged "operator": their access is not a role and cannot be changed there. Agents started with the CLI
belong to the operators. Every action, allowed or refused, is in the project's audit log.

`admin` is an operator. An operator makes or removes others under **Settings, Users**; the last operator cannot be removed. If
every operator's sign-in is lost, see [Locked out](#locked-out) above.

There is no messaging in the web UI: talk to an agent in its terminal.

