# Container logs follow the engine's default, and agent status needs a readable log

Status: **decided for v1, not built yet** (written 2026-10-15).

## What egzo v1 does

- egzo uses the **engine's default logging configuration** (Docker's `daemon.json`, Podman's `containers.conf`) for every
  container it creates: agents, the control sidecar and the proxy. It does not choose a log driver and it does not set log
  options. `egzo.yaml` has no logging key.
- **Bounding the logs is the sysadmin's job.** Docker's default `json-file` driver has no size limit (`max-size` is `-1`), so a
  chatty agent can fill the disk. Configure the engine's default so logs are bounded, for example `log-driver` and `log-opts`
  with `max-size` and `max-file` in `daemon.json`, the `local` driver, `journald` with the journal's own retention, or Podman's
  `log_size_max` in `containers.conf`. A change to the default applies only to containers created after it, so recreate the
  project (`egzo project down`, `egzo project up`) to pick it up.
- Sysadmins choose `journald` and others for good reasons. An egzo that overrides the choice is a surprise.

Today the code does the opposite: `BoundedLogs` (`internal/stack/desired.go`, `internal/stack/apply.go`) pins `json-file` with
`max-size 10m`, `max-file 3` on every container, and the spec `test_the_sidecars_run_unprivileged_with_limits_and_bounded_logs` and
`test_an_agent_cannot_fork_without_limit` assert it. Removing the pin, and those assertions, is part of this change.

## Later

A logging config like docker-compose's: a `logging:` key with a `driver` and `options`, on a service or for the whole project,
overriding the engine's default where the user asks for it. Not in v1. It is a schema change, so specs first.

## Why the log has to be readable

The hub (and egzo itself) learn what an agent is doing (activity, a waiting question, the status line) from the control sidecar.
Asking it with `docker exec` on every scan costs too much, so the plan is that control writes its status as lines in its own log
and the hub follows that log through the engine socket (`ContainerLogs` with `follow`). That needs a log the engine can stream
back. Without one the hub cannot tell an agent's status, and shows it as unknown (never "starting").

## Known not to work

Checked on this machine (Docker with the daemons' own drivers, Podman 5.7.0) unless marked as reported.

- **Docker, `none`:** `docker logs` fails with `configured logging driver does not support reading`.
- **Podman, `none`, `passthrough`, `passthrough-tty`:** `podman logs` fails with `cannot read logs: this container is not logging output`.
- **Docker, a remote driver with the local cache turned off** (`cache-disabled: true`): `syslog`, `gelf`, `fluentd`, `awslogs`,
  `splunk`, `gcplogs`, `etwlogs`. Docker reads them only through its dual-logging cache, on by default since Docker 20.10. An engine
  older than 20.10 has no cache, so these drivers cannot be read there at all (from Docker's documentation, not tested here).
- **Docker, a remote driver whose endpoint is down or refuses:** the cache can stop receiving lines. Observed with `gelf` pointed at
  a closed UDP port: the first line was cached, later ones were not, even in a plain `docker logs`.
- **`journald` when the journal is not readable by the engine's user.** Reported for rootless Podman: `initial journal cursor`
  errors, or `podman logs` printing nothing while `journalctl` shows the lines (Red Hat solution 7009652, a NixOS forum thread, a
  Podman issue about the driver failing silently inside Podman-in-Podman). Docker and Podman both streamed `journald` correctly
  here, with `--follow`.

Known to work: Docker `json-file`, `local`, `journald`, and the remote drivers through the dual-logging cache while the endpoint is
healthy (tested with `syslog`); Podman `k8s-file` (which `json-file` is an alias for) and `journald`.

## What egzo will do about it

Say so: a project whose engine cannot stream the log of its control sidecar gets a status of `unknown` in the hub, and
`egzo project up` warns, naming the driver. It is not an error: the agents run, and the terminal, start, stop and remove all
work. Only live status and the "waiting" question are missing. See also
[control-compatibility](../roadmap/control-compatibility.md), where an outdated control that prints no status lines is the same
signal.
