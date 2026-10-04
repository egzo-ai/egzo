# Working on egzo

## Specs are the todo list: a spec for an unimplemented feature FAILS

`specs/` is the specification. A spec for something that is not built yet must be red. Never make it
green, hide it, or soften it:

- **Never** mark a spec `xfail` (marker or `pytest.xfail()`), `skip` or `todo` because the feature is not
  implemented, not decided, or blocked. A blocked spec fails with a message that says what it is blocked on
  (`pytest.fail("blocked on ...")`).
- `xfail` is for **unsupported platforms only**: a spec that uses a feature a reference platform lacks, such as
  gVisor on a platform without it (the `gvisor_agents` fixture). It says "this platform cannot do it", never
  "we have not written it yet".
- `skip` is for specs that do not apply to the platform (a spec about Podman on Docker).
- A red suite after you add specs is the correct state. Say which specs fail and why; do not make them pass by
  loosening them. Implement the feature, or leave them failing and report it.
- When a spec starts passing because the feature landed, nothing else needs to change.

## Specs run on a reference platform

One run tests one platform (`--engine`, or chosen by `specs/select_platform.py`). A platform describes the
host; it does not ask egzo for anything. See `specs/README.md`.

## Other rules

- Docker is the engine to run the specs against day to day; do not run the whole suite more than needed
  (about 7 minutes).
- Commit when something meaningful works.
