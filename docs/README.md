# egzo documentation

Egzo is early, and has not been released: see [`roadmap/initial-release.md`](../roadmap/initial-release.md) for what the docs
still have to be checked against. These pages are quick pointers, not a reference; `egzo <command> --help` and `egzo config` are the
detail, and the executable specs in `specs/` are the status report.

**Start here**
1. [Introduction](intro.md): what egzo is, how it is built, a first run
2. [Installing and choosing an engine](install.md): build, Docker, Podman, gVisor, `egzo doctor`

**Using egzo**
3. [The project file](project-file.md): `egzo.yaml` by example
4. [Managing projects](managing-projects.md): `up`, `spawn`, `attach`, `rm`, `down`
5. [Git workspaces](workspaces.md): clone, worktree, shared, and keeping work safe
6. [Messaging](messaging.md): sending tasks, questions from agents, agents talking to each other
7. [Supported harnesses](harnesses.md): Claude Code and OpenCode
8. [Overriding base images](images.md): adding tools to an agent

**Safety and problems**
9. [Sandboxing agents](sandboxing.md): writing egress policies
10. [The security model](security.md): what is enforced and what is not
11. [Troubleshooting](troubleshooting.md)
