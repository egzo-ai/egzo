# Rootful Podman on AppArmor hosts: exec'd processes cannot reach the container's init

Status: **open, worked around in CI** (found 2026-10-16). Host-level (Ubuntu 26.04, Podman 5.7.0, crun, AppArmor
profile `containers-default-0.66.0`), not an egzo bug as far as we can tell.

## Symptom

On rootful Podman, once a container has `no-new-privileges` (egzo sets it on every container), the processes
that `podman exec` starts and the container's own processes are in different AppArmor contexts:

- init and the container's processes: `containers-default-0.66.0`
- an exec'd process: `containers-default-0.66.0//&crun (mixed)`

They cannot ptrace or signal each other even with the same uid. A reproduction without egzo:

    sudo podman run --rm --init --user 1001:1001 --security-opt no-new-privileges alpine:3 \
      sh -c 'ls -l /proc/1/exe; echo x > /proc/1/fd/1'     # Permission denied on both

Without `no-new-privileges`, or with `--security-opt apparmor=unconfined`, both work. Docker (docker-default)
and rootless Podman are not affected.

Specs that failed because of it on the `podman` platform: reading another process's cwd, writing to
`/proc/1/fd/1` through `egzo agent exec`, and the browser specs (`kill EACCES` in the MCP client).

## What the agents see

Processes started by the agent's own tree are in one context and work. What breaks is a process started through
`exec` (an operator's `egzo agent exec`, a spec) acting on one the container started.

## Workaround

CI sets `apparmor_profile = "unconfined"` in `containers.conf` for the rootful Podman cell (`scripts/ci-engine.sh`).
That runs the cell without AppArmor, which is what the specs of the other platforms in effect assume.
