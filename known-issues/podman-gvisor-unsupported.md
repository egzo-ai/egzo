# gVisor on Podman is not supported

Status: **will not fix** while Podman's API stays as it is (written 2026-10-04).

egzo talks to the engine through the Docker Engine API (Podman through its Docker-compatible socket).
That API lets a client ask for an OCI runtime per container (`HostConfig.Runtime`), and Docker applies
it. Podman's compatibility layer ignores the field, so a client cannot choose, and cannot verify, which
runtime a container gets. The only way to run Podman containers under gVisor is to make `runsc` Podman's
default runtime in `containers.conf`, which is a host setting egzo cannot control or check.

Because the runtime is a security boundary, egzo does not start an agent that asks for one on Podman:
`egzo up` fails and says why (`internal/stack/up.go`, spec
`test_podman_refuses_a_runtime_because_it_cannot_apply_or_verify_it`). Silently starting the agent in the
default runtime was the earlier behaviour, a warning only, and it was a downgrade nobody chose.

So there is no `podman-gvisor` reference platform, and its specs are neither written nor run. This changes
if Podman's compatible API starts honouring the runtime, or if egzo gains a Podman-native client.
