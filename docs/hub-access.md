# Hub access

Who gets into [the hub](hub.md), and what each person may do there.

**An account takes an invite.** A person needs a way to sign in **and** an invite: an account at your identity provider is not enough, so
connecting a company-wide provider does not let the whole company in. On the hub, a role is held on a project, and operators look after the hub
itself.

## The starter account

`admin` signs in with a password, for starting out and for demos. There is no page to change its password.

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

Signing in with the provider is not enough: somebody the hub does not know is refused, and nothing is stored for them, until they come
with an [invite](#invites).

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

## Invites

An invite is a link with a one-time code. It is the only way to get an account.

- **It is good once**, for 7 days, and can be revoked until then. The link is shown once, to whoever makes it. The hub keeps only a hash of the
  code, which is 128 random bits. Sending the link is up to you: the hub sends no email.
- **Nothing ties it to a person.** It is redeemed by whoever opens it and signs in, so hand it over as you would a password. The audit log says
  who made it and who used it.
- **It carries what it grants:**
  - **a role on a project**, made by the project's owner on its Members tab;
  - **an account, with the flags below** (operator, project quota), made by an operator under Settings, Invites.
- **An invite only adds.** It raises a role and never lowers it, and a quota only grows. An owner who opens a viewer's link stays an owner.
- **It is worth what its maker can still give.** An invite is deleted when its maker is disabled, is no longer an owner of the project, or is no
  longer an operator, and when the project is revoked or leaves the hub. Giving the right back does not bring it back, and the audit log has an entry for it.

Whoever opens the link sees what it grants and who sent it, then, depending on how the hub is set up:

- **Continue with the provider**: their account at the provider becomes a new user, and the invite is spent. If they already have one, they
  sign in, and it is added to that user.
- **Create an account**: a username and a password of 12 characters or more. This is the way in for somebody from outside, such as the viewer of a
  preview, who has no account at your provider. It needs `EGZO_LOCAL_LOGIN` on, which is its default: with it off, nobody signs in with a
  password, whoever they are. There is no password reset: a person who lost theirs needs a new invite, which makes a new user. An operator disables the old
  one.
- **Accept**: somebody signed in already adds it to their user.

## People

- A person who comes through the provider gets a **username** the hub picks from what the provider says
  (`preferred_username`, then the email's local part, then the name), with `-2`, `-3`... when it is taken. It never changes after.
- Nothing is matched by email. A person is their account at the provider (its issuer and subject); the email and name are shown,
  never trusted.
- The provider's picture is shown as the avatar. It is fetched by the browser, never by the hub, so the hub's content security
  policy allows https images, and the picture's host sees who views the page.
- **Disabling** (Settings, Users) signs a person out, closes their terminals and refuses their sign-in, by password and by SSO alike.
  Their roles stay, unused, until they are enabled again; their agents keep running, and their projects stay linked. People are never deleted:
  the audit log and the agents' labels keep naming them. An operator cannot be disabled; remove operator first.
- A person who signed in with SSO before linking it to `admin` is a second user: disable that user, use **Remove** on its account
  under Settings, Users, then link again.

## Changing the identity provider

The hub trusts the one issuer it is configured with.

- Accounts of the old provider can no longer sign in, and their sessions end at the next start (the log names both issuers).
- Nobody at the new provider is known to the hub, even with the same email: each needs a new invite, which makes a new user with a new
  username (`-2` when the old one is taken).
- Whoever can still sign in with a password (in practice `admin`) moves their own account: Link the new provider, then Unlink the
  old one. Their roles and agents stay.
- Everyone else is invited again by each project's owners; `admin` invites the other operators. Then disable the old users.
  Disabling an owner can leave a project with no owner: see [Operators](#operators).
- In order: set `EGZO_LOCAL_LOGIN=true` and `EGZO_ADMIN_PASSWORD`, change the issuer and restart, sign in as `admin`, link the new
  provider and unlink the old, invite the team, disable the old users, turn local login off.
- Entra ID gives each app registration its own subjects: registering the app again is the same as changing provider.

## Locked out

`EGZO_LOCAL_LOGIN=true` with `EGZO_ADMIN_PASSWORD` set, and a restart, always gets `admin` back in as an operator, whatever
happened to the provider or the password. With local login off and no operator who has an account at the configured provider,
the hub starts and says so in its log; with local login off and no operator at all, it does not start. `admin` comes back with what it had:
to reach a project's terminals or logs it [gives itself a role](#operators), like any operator.

## Roles

A role is held on one project. Roles live in the hub's database; the project file knows nothing about them.

| Role | May |
|---|---|
| viewer | open the web preview of any agent of the project, from the link they were given. Nothing else: the project is not listed for them, and they see neither its agents nor its previews. A preview over the link is [not built yet](../roadmap/hub-link.md#order-of-work), so today the role opens nothing |
| user | see the project and its agents; spawn agents; start, stop, remove and open the terminal of their own, and read their logs and network |
| admin | the same on every agent of the project, those spawned from the CLI included; read the audit log |
| owner | an admin who also grants and removes roles, invites, and can unlink the project |

- **An owner's destroy is an unlink, and nothing more.** The project's Members tab has it. The hub forgets the project, with its roles, its tasks and its
  network history, and the link hears it is not registered. The project keeps running on its machine until somebody runs `egzo project down`
  there: the hub never does. A new link code brings it back, as a new project with no roles. The audit log stays.
- **A project keeps at least one owner.** An owner cannot remove or lower themselves, and an owner who is the last one who can sign in stays one. An owner
  can change other owners.
- Agents spawned from the CLI have no person behind them on the hub: admins reach them.

## Operators

An operator is a flag on a user, not a role. `admin` is one. An operator makes or removes others under **Settings, Users**, or invites one; the last
operator cannot be removed.

- **An operator oversees.** They see every project, its owners, its agents and their state, and the links. They manage the users, the invites and the
  quotas, and can revoke any project.
- **To go further they give themselves a role.** A terminal, logs, a project's network, its timeline and its audit log take a role on the project,
  as they do for anybody. An operator grants it on the project's Members tab: it is written to the project's audit log and listed there like any
  grant, so the owners can see it. An owner can remove it. An operator can take their own role back, even an owner's, as long as the project has another
  owner.
- **A project left without an owner is theirs to deal with.** Disabling a person can leave a project with no owner who can sign in. It stays linked,
  and Home, Settings, Links and the project itself say so to the operators, who make somebody its owner on the Members tab, or revoke it.

## Project quotas

The quota on a user, under **Settings, Users** or in the invite that made them, says how many projects they may connect to the hub.

- **0, the default, cannot connect one.** An operator is not held to a quota.
- It is checked when a person makes a link code, and again when the code is spent: two codes are not two projects. A code that finds the quota used up
  is refused and stays good until it expires, so it works if a project leaves first. The link hears it is not registered, and the audit log says why.
- **It can be over-subscribed.** Lowering it under what a person has removes nothing: it stops new ones.
- What counts is the projects the person connected that are on the hub, whoever owns them now. Being invited as an owner costs nothing, and a project that
  was revoked or unlinked no longer counts.

Everything anyone does, allowed or refused, is in the project's audit log.

There is no messaging in the web UI: talk to an agent in its terminal.

