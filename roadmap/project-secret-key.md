# Roadmap: a project secret key, for what egzo keeps at rest

**Status.** Decided, not built: the specs will be the status. No backward compatibility. A prerequisite of
[oauth-services.md](oauth-services.md).

## The problem

egzo keeps a secret of its own on disk: the project CA's private key, in a volume only the proxy mounts, in clear. A copy or a
backup of the volume gives the key that every agent trusts. [OAuth services](oauth-services.md) add more: sign-in tokens that
rotate, which cannot live in a vault and must survive a proxy restart.

## The decision

Every project names a secret key, read from a vault like any secret:

```yaml
secret_key: main/egzo/shop-key

vaults:
  main: { backend: pass }
```

It encrypts what egzo keeps at rest: the CA's private key, and any other secret egzo stores (OAuth sign-ins first). The user
generates it once and keeps it in the vault, as for the hub's `EGZO_SECRET_KEY`:

```console
$ openssl rand -hex 32 | pass insert --multiline egzo/shop-key
```

- **Required.** `project check` and `up` refuse a file without it and print the command above with the file's own vault.
  `project init` writes the line and prints the command.
- **Opaque, at least 32 bytes.** egzo does not parse it and cannot tell a wrong key from a right one until something sealed with
  it fails to open.
- **Per project or shared: the user's choice.** The keys egzo uses are derived from it and the project, so one secret can serve
  several projects without them sharing a key.
- **How it reaches the proxy.** With the policy, through `engine exec` stdin at `up`, `diff` and `restart proxy`; in memory only,
  like every secret. A restarted proxy opens nothing until the next `up`, as today.

## How things are sealed

- **Keys:** HKDF-SHA256 from the secret key, the project as salt, one label per use (`egzo/v1/ca`, `egzo/v1/oauth`).
- **Cipher:** XChaCha20-Poly1305, a random nonce per write. The associated data names the project, the use and the format
  version, so a sealed record cannot be moved to another slot or project.
- **A key check** (an HMAC of a fixed label under its own derived key) is stored with the sealed data, so a wrong key is told
  apart from corrupt data.
- **A volume copy or backup is useless without the vault secret.**

## Changing or losing the key

- **The CA.** Sealed with another key, it cannot be opened: `up` stops and says so. Two ways on: give the old key back, or
  `egzo project rotate-ca` (a new CA, agents restart).
- **OAuth sign-ins.** Discarded; the user signs in again.
- Re-sealing under a new key (`up` given the old and the new) is later.

## Existing projects

The first `up` with a `secret_key` seals the CA key already in the volume: same CA, no agent restart. A project without
`secret_key` is refused with the instructions, like any other missing field.

## Specs and docs

- Config specs: no `secret_key` is refused with the command to create it; a short key is refused; the reference follows the rules
  of any secret.
- Engine specs: the CA key in the proxy's volume is not in clear; a project made before this keeps its CA after `up` with a key;
  another key stops `up` with the two ways on; `rotate-ca` under a new key works.
- Docs: `docs/project-file.md`, `docs/security.md` (what is encrypted at rest, and with what), `docs/managing-projects.md`
  (creating and keeping the key), DESIGN.md.

## Alternatives considered

- **A key per service** (`store_key` on an OAuth service): the key belongs to what egzo keeps, not to a service.
- **On a vault** (`vaults.main.secret_key`): ties a project setting to one vault.
- **A naming convention, no field:** the file stops listing what egzo reads.
- **A key pair, the proxy holding the public half:** the proxy must read its own data back after a restart.
- **A random key kept in the volume:** clear text by another name.
- **Envelope encryption** (the vault secret wraps a data key in the volume): only helps rotation, and re-sealing the few records
  there are costs nothing.
