# Roadmap: detect incompatible controls and report their status

**Status.** Open, not designed. **Needed for 1.0.0.** Not before.

The hub learns an agent's activity from its project's control sidecar (`GET /v1/agents`, `GET /v1/events`, ...). A project
brought up by an older egzo keeps its old control sidecar until `egzo project up` runs again, and that sidecar may not have
the endpoint the hub calls (a `404 page not found`). The hub swallows the error, so the agent shows as **Starting**
for ever (`activityOf` in `hub/internal/engines/docker.go`, `Rows` in `internal/stack/apply.go`), even though it works, and the
events panel logs an error every few seconds. Stopping and starting the agent does not help: only the agent container is
recreated, not the sidecar.

The labels do not separate the two cases. An old and a new control both carry `ai.egzo.spec-version=1`, and `ai.egzo.created-by`
is a build string (`egzo/0.0.1-rc1`, or a bare `egzo/dev` for an unreleased build) that cannot be compared.

The need: the hub and the CLI tell an incompatible or unreachable control from a healthy one, say so on the agent and the
project, and name the fix. This will keep coming back as the control API grows, so the rule for changing it belongs here too.

Everything below is open. Pick the smallest thing that works.

## Ideas

- **Control reports what it speaks.** A `GET /v1/version` returning `{version, api, capabilities}`: `api` is one integer that goes up
  only when an endpoint is removed or changes shape; `capabilities` is a list of names (`agents`, `events`, ...). A control
  without the endpoint answers 404, and that 404 means "api 0, too old". It works on builds with no useful label.
- **The hub decides.** It declares the `api` it needs, and the capability it wants for each feature. The control knows nothing
  about the hub. A missing optional capability turns off one feature, not the project.
- **Typed result from every control call**: `ok`, `unreachable`, `outdated`, `error`, instead of a swallowed error. An agent's
  activity is then `stopped`, `outdated` or `unreachable`, or `starting` only when control answered but the agent has not reported.
- **Show it.** A distinct state on the agent, the project and the services ("control outdated, run `egzo project up` again"),
  with the control's version on the project page. One failing endpoint degrades one panel with an explanation, and logs once, not on every poll.
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
