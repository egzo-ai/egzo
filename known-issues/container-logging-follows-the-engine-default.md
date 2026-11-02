# Container logs follow the engine's default, and agent status needs a readable log

Status: **built** (written 2026-10-15; the hub follows the log, `egzo system check` tries the driver).

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
- **The control sidecar adds a line to its log every 30 seconds** (the status heartbeat), and one for each change of an agent's state,
  coalesced to at most a few a second: a few hundred kilobytes a day for a project, which a bounded log absorbs and an unbounded
  one keeps. `EGZO_STATUS_INTERVAL` on the sidecar changes the heartbeat.

Before this, egzo pinned `json-file` with `max-size 10m`, `max-file 3` on every container (`BoundedLogs`). That is gone, and the
specs now assert the opposite: every container has the logging a container made by hand on that engine has
(`test_every_container_uses_the_engines_default_logging`). `egzo system check` warns when the default is a `json-file` with no limit.

## Later

A logging config like docker-compose's: a `logging:` key with a `driver` and `options`, on a service or for the whole project,
overriding the engine's default where the user asks for it. Not in v1. It is a schema change, so specs first.

## Why the log has to be readable

The hub learns what an agent is doing (activity, a waiting question, the status line) from the control sidecar, and asking it with
`docker exec` on a timer costs too much. So the control sidecar writes its status as JSON lines on its **standard output**: one on
every change, and one every 30 seconds (`EGZO_STATUS_INTERVAL` on the sidecar), each a complete snapshot of the project's agents.
Everything else it says goes to standard error, as JSON. The hub follows the container's log through the engine's log API
(`ContainerLogs`, standard output only, `follow`), one stream per project, and keeps the newest line. That needs a log the engine can
stream back; the engines tell the two streams apart whatever the driver. The same reading is already needed by the git jobs
(`prep` containers return their output through the log) and by `egzo logs`, so a driver that cannot be read breaks those too, and
`egzo system check` fails on it. A log that cannot be followed makes the hub show the project's agents as **running**, with a
banner that says why, and never "starting".

**What `system check` does about it:** it reads the engine's default driver (`/info`, which Podman leaves empty), and when the egzo
image is on the engine it runs a throwaway container that writes to both streams and follows its log: it reports whether the
log was readable, followed while the container ran, and kept the streams apart, and what driver and options the engine gave the
container. Without the image it can only judge the driver by name, and says that it did not try.

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

## What egzo does about it

- `egzo system check` fails on a driver it cannot read (`none`, Podman's `passthrough`), warns when it cannot tell, and warns about a
  `json-file` with no size limit.
- The hub shows a project whose status log cannot be followed as: its agents running, with a banner that carries the reason (the
  engine's error, or "the control sidecar writes no status"). The terminal, start, stop and remove all work. Only live activity and
  the "waiting" question are missing. An outdated control sidecar that writes no status lines is the same signal
  ([hub-link: Versions](../roadmap/hub-link.md#versions)).
- `egzo project up` does not check the driver; `egzo system check` does.
