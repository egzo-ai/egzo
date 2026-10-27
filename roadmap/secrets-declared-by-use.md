# Roadmap: a secret is declared where it is used

**Status.** Decided, not built: the specs will be the status. No backward compatibility. A prerequisite of
[project-secret-key.md](project-secret-key.md).

## The problem

Every secret is written twice: once in its vault's `secrets` list, once where it is used (`services`, and soon `secret_key`).
A reference to a secret no vault lists is an error, so the list is pure repetition.

```yaml
vaults:
  main:
    backend: pass
    secrets: [anthropic/api-key]          # here

egress:
  default:
    services:
      anthropic: main/anthropic/api-key   # and here
```

## The decision

A reference declares the secret. A vault is a name and a backend; `secrets` is gone.

```yaml
vaults:
  main: { backend: pass }

egress:
  default:
    services:
      anthropic: main/anthropic/api-key
```

- **What egzo reads is still in the file**, at the place it is used. `egzo project check --resolved` lists every secret the file
  refers to, by vault, as the resolved `secrets` of each vault.
- **A reference to an unknown vault** stays an error, naming the vaults that exist.
- **`secrets ls`** reports every referenced secret (`set`, `empty`, `missing`), as today.
- **A vault nothing refers to** is a warning at `project check`, not an error.
- The rules on names do not change (`env` secret names are variable names, a pass name never starts with `-`).

## Specs and docs

- Config specs: `secrets` on a vault is refused (no backward compatibility); a reference to an undeclared name is accepted and
  appears in the resolved file; an unknown vault is refused; an unused vault warns.
- `secrets ls` lists the referenced secrets.
- Docs: `docs/project-file.md` (vaults), `docs/sandboxing.md`, every example; DESIGN.md "Secrets and vaults".

## Alternatives considered

- **Keep the list as a review point** ("everything egzo may read, in one place"). `project check --resolved` gives the same view
  without the repetition.
- **An exception for `secret_key` only.** Leaves the repetition everywhere else.
