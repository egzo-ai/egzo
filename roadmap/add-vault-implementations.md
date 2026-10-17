# Roadmap: more vault implementations (idea, not decided, not built)

Today there are two backends, `env` and `pass`. This item adds more, and one small change to the design so they
can be configured.

## Design change: an `options` bag on vaults

```yaml
vaults:
  main:
    backend: sops
    options: {file: "./sops.json"}
    secrets: [anthropic/api-key]
```

- `options` is a map of scalar strings. Each backend declares the keys it accepts; an unknown key is a
  validation error naming the keys that exist. A backend with no option to offer (`env`) accepts none.
- Relaxes `DESIGN.md`, "Secrets and vaults": "the project file cannot configure pass" came from pass already having
  `PASSWORD_STORE_DIR` and `GNUPGHOME`, not from a rule against configuring a store. Backends that already have a
  standard environment configuration keep using it; options are for stores where the project is the natural place
  to say where the secrets are.
- Everything else in the design holds: a vault is a backend and the names to read, `<vault>/<secret>`, egzo only
  reads, secrets are read on the host by the CLI, unlocking is the user's own tooling. A backend is added when it
  works, together with its specs.
- Not options: free-form arguments for the backend's binary. Backends that send a credential to a remote service
  (an address option) need a decision of their own when they are added.
- Secret name validation (`secretNameProblem`) becomes per backend.

## Candidates

- **sops** (first, it needs `options`): `file`, secret name is the path inside the file.
- **gopass**: same shape as pass.
- **OS keystores** (macOS Keychain, Secret Service): also wanted by [macOS and Windows hosts](windows-and-macos.md).
- **1Password CLI** (`op read`).
- **HashiCorp Vault / OpenBao**.
- **Cloud secret managers** (AWS, GCP, Azure), on demand.
