# Roadmap: hub tasks, feedback for actions that take time

**Status.** Built, as below; "Not now" is open. From [#54](https://github.com/egzo-ai/egzo/issues/54).

Every action of the hub on a project can take from a second to a minute: spawn, start, stop, remove. This page was written when the hub still called engines, so
read "engine" as the project's link. Before tasks:

- **Spawn** answers at once and runs in the background, but the project lists the spawn as a row of its own, with no name when
  none was asked. The engine's events bring the agent's row while the spawn still runs, so for a while there are two rows, then
  the spawn's row goes away.
- **Start, stop and remove** are synchronous requests with nothing on the screen while they run. A removal waits for the project
  lock, removes the container, the network and the volumes, and lists the engine again before it answers.
- Only whoever clicked learns how it ended, and only through a toast. Nobody else sees that something runs on an agent.
- What runs lives in memory: a hub that restarts forgets it.

## Principles

- **egzo is an ops tool: the screen shows the system's real state.** No optimistic UI: nothing changes on the screen before the
  hub has accepted the request (a button is disabled until the answer, which takes milliseconds). A task is itself real state: "the
  hub is stopping agent-3, asked by alice 4 seconds ago" is a fact, shown next to the container's own state, never instead of it.
- **One mechanism for every action that calls an engine.** Reads (a project, logs, events, the spec) are not tasks: they keep
  their loading states.
- **Shown where people look, not in a new place to watch.** The task is part of the agent's status, wherever the agent is shown.

## Tasks

- Every action that calls an engine **answers 202 with a task**: id, actor, verb, engine, project, agent, state (running, done,
  failed, interrupted), the current step, the error, started, ended (`hub/internal/hub/tasks.go`, which replaced the spawns of
  `spawn.go`). Spawns and removals of a project run one at a time, in order: both take the engine's project lock.
- **Tasks are kept in the hub's database.** A task that ran when the hub stopped is marked `interrupted` at start-up. A failed
  one stays until it is dismissed; a new task on the agent takes it off by itself. The audit log stays the record of who was
  allowed to do what, the task is what happened.
- **One task per agent at a time.** A second one is refused with 409, naming the running task and who started it.
- **Steps.** `stack.Spawn` and `stack.RemoveInstances` already write what they do ("create container ...", "remove volume
  ..."); the Docker engine sends it to `io.Discard`. The `Engine` methods take a progress callback and the latest line is the
  task's step. No change to core.

## The task is part of the agent's status

Every agent of the project view carries its task (`task` in the API): the one that runs, or the failure nobody dismissed. The
agent's own activity stays what the status feed says; the task is shown next to it, as a pill (`spawning`, `starting`, `stopping`,
`removing`, or `stop failed`) with its step and who asked. The project table, the agent page and the terminal tab in the sidebar
show it, and their buttons wait while it runs.

## A spawn is one row from the start

- **The spawn modal requires a name**, filled in with a suggestion the user can change. The task is keyed by the name from the
  first second.
- **A spawn task whose container does not exist yet adds an agent** with its name, its template, no state and the task. When the scan
  finds the container, the real agent takes its place under the same name and the task stays on it until it ends. The separate
  list of spawns (`spawnsOf`, the `pending` rows in `Project.svelte`) goes away.
- **Suggested names are not numbered.** Removed agents keep their branch, so a name such as `coder-3` is often refused (see
  CLAUDE.md, "Auto-chosen instance names skip numbers"), and the hub cannot see branches. The suggestion is a screen name, the
  way Docker names containers: `<template>-<adjective><noun><digits>`, such as `coder-sunnycar2332`, from short word lists
  checked for unfortunate pairs, avoiding the names in use. A clash with an old branch is then rare, and fails like any other
  spawn error. The CLI keeps its numbered names.
- **After the modal, the agent's page.** It shows the steps as they happen and opens the terminal when the agent is made, if the
  user is still on that page.

## Activity

- **An Activity tab on the project**: the tasks that run first, then the history, with who, what, the result and how long it
  took. Several people working on one project see what the others are doing.
- **The agent page** lists that agent's tasks.
- **Toasts** only for whoever started the task, and only when the agent is not on the screen or the task failed. A task's toast
  is one toast, updated from the step to the result, with a link to the agent.
- **Not on Home.** The project list does not mark a project where something runs; the agents' own pages and the sidebar do.

## Not now

- **Pushing to the browser.** Tasks fit the 3-second poll of the project page, with a load right after the 202. A stream of
  the project view (server-sent events) would make the end of a task show within the hub's event debounce; separate.
- **Undo instead of confirm.** Not for egzo: a removal deletes the agent's workspace.
- **Returning before the slow part is done** (the container gone, the volumes cleaned up after). Possible later, if a step
  turns out to be what users wait for.
