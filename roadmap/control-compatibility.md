# Roadmap: detect incompatible controls and report their status

**Status.** Partly built (the status feed, see below); the rest is open and **needed for 1.0.0**. Not before.

The hub used to learn an agent's activity from its project's control sidecar by running a command in it (`GET /v1/agents`), and a sidecar
made by an older egzo that lacked the endpoint answered `404 page not found`: the hub swallowed the error and showed the agent as
**Starting** for ever. Stopping and starting the agent did not help, since only the agent container is recreated, not the sidecar.

**Built so far (see the [known issue](../known-issues/container-logging-follows-the-engine-default.md)).** The hub no longer asks the
sidecar: it follows the status the sidecar writes to its log, and a project is `live`, `connecting`, `stale` or `unavailable`. A sidecar
that writes no status lines, which is every one made before the status log, is `unavailable` ("the control sidecar writes no status: it
is older than this hub, or its log cannot be read") after 90 seconds, and its running agents show as running, never "starting". A log
that cannot be read at all is `unavailable` with the engine's error. That covers the symptom, and tells an old sidecar from a
healthy one, but not **why**: an old control and an unreadable log look the same, and nothing compares versions.

The labels do not separate the two cases. An old and a new control both carry `ai.egzo.spec-version=1`, and `ai.egzo.created-by`
is a build string (`egzo/0.0.1-rc1`, or a bare `egzo/dev` for an unreleased build) that cannot be compared.

The need that remains: the hub and the CLI tell an *incompatible* control from an unreadable log, name the fix, and the rule for
changing the control API belongs here too, because this will keep coming back as it grows.

Everything below is open. Pick the smallest thing that works.

## Ideas

- **Control reports what it speaks.** A `GET /v1/version` returning `{version, api, capabilities}`: `api` is one integer that goes up
  only when an endpoint is removed or changes shape; `capabilities` is a list of names (`agents`, `events`, ...). A control
  without the endpoint answers 404, and that 404 means "api 0, too old". It works on builds with no useful label.
- **The hub decides.** It declares the `api` it needs, and the capability it wants for each feature. The control knows nothing
  about the hub. A missing optional capability turns off one feature, not the project.
- **Typed result from every control call**: `ok`, `unreachable`, `outdated`, `error`, instead of a swallowed error. (The feed states
  `live`, `stale` and `unavailable` are the first half of this; `outdated` is the part still missing, and needs the version.)
  Put the version in the status line itself (`{"type":"status","v":1,...}` already has a format version), so an old sidecar
  that writes no line and an unreadable log can be told apart once a version is there.
- **Show it.** A distinct state on the project and the services ("control outdated, run `egzo project up` again"), with the control's
  version on the project page. (The banner for an unavailable feed exists; it cannot yet say "outdated".) The timeline, spec and
  templates are still read by running a command in the sidecar, and an old sidecar's `404` there is reported as an error of that panel.
- **The CLI too.** `egzo agent ls` and `egzo project status` flag an outdated control, and `egzo doctor`, if there is one, checks it.
- **Labels as a hint only.** Compare `created-by` or the image version with the running egzo when both are releases; say
  "built by 0.0.1-rc1" and never block on it.
- **The change rule.** Adding an endpoint or a field is free. A change or removal bumps `api`. Support a window (the current
  api and one older), written down and checked by a spec.
- **Does `project up` fix it?** Check that it recreates a sidecar whose image tag is the same but whose build is older. The hub
  never changes a project, so it only names the command.
- **Same gap on agents.** `Stale` covers a template that changed, not an image built by an older egzo. A version label on the
  harness image and a "built by an older egzo" flag would cover it.
- **Open:** one `api` number or one per capability (start with one); how wide the window is.

## Tests

- Core: specs first (`specs/`): the version endpoint is present, `api` is an integer, an unknown path still answers 404.
- Hub: a Go unit test with a fake engine that answers 404 to `/v1/version` and checks `outdated`, not `starting`.
