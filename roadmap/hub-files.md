# Roadmap: the project's files in the hub

**Status.** Built (November 2026) on branch `hub-files`, on top of PR 88 (the hub's project-first navigation). Docs:
[hub.md, Files](../docs/hub.md#files). Open: the permissions below, and the external volume kind when it lands.

## Why

A person on the hub sees an agent's terminal, its network and its logs, and nothing of the files it works on. `egzo workspace get`
copies them out, from the project's machine only. The hub should show a project's workspaces and let a person download a file, with
the least code that does it.

## What it is

A **Files** section in a project's left nav. It lists the project's workspaces; a workspace opens on a plain table of its files
(name, size, modified), a folder opens, a file downloads. Nothing else: no viewer, no editor, no upload, no search, no git.

| Kind | What is shown | Opens |
|---|---|---|
| Directory `{}` | the volume `<p>_ws.<name>` | yes |
| Git `{git: ...}` | the repository, with a row per worktree under it, named after its instance: running, stopped or removed | the repository: no (the base clone has no files checked out). A worktree: yes |
| External `{volume: ...}` (planned, [support-engine-named-volumes.md](support-engine-named-volumes.md)) | the volume, mounted as agents mount it: its `subpath` only, read-only if `readonly` | yes, once that kind is built |

## A worktree outlives its agent

`egzo agent rm` and `egzo project down` no longer delete worktree volumes: what an agent left uncommitted stays, and shows in Files as
the worktree of a removed agent.

- `egzo agent rm --worktrees NAME` deletes them with the instance.
- `egzo workspace rm WORKSPACE --worktree INSTANCE` (built) deletes one later.
- The base clone forgets a worktree only when its volume is deleted: a worktree that is kept stays a worktree, and `agent spawn --reuse
  NAME` continues in it.
- A name with a worktree left is not reused (already the case for a branch: see CLAUDE.md, "Auto-chosen instance names skip numbers").

## How a file travels

```
browser --GET--> hub --link stream "files"--> link --exec--> files helper container --unix socket--> egzo files serve (/w)
```

- **The hub** checks the person's role, takes `GET` and `HEAD` only, forwards `Range`, `If-Range`, `If-None-Match` and
  `If-Modified-Since` and nothing else, and sends files with `Content-Disposition: attachment`, `X-Content-Type-Options: nosniff` and
  `Content-Security-Policy: sandbox`: a file an agent wrote never runs on the hub's origin.
- **The link** has two new verbs. `workspaces` lists the workspace volumes it finds, with the worktrees and the state of their
  instances, and a git workspace's remote and branch from the templates `up` last published (https only, no credential, query or
  fragment; the hub checks it again and shows it as text, never a link). `files` names a workspace (and an instance, for a worktree) and becomes a byte stream, as `forward` does for a preview.
- **The helper** is one container per volume, made on the first connection: the egzo image, the agents' uid, the volume read-only at
  `/w`, no network, a read-only root, every capability dropped, `no-new-privileges`, small memory, pid and CPU limits. Its main process
  is `egzo files serve`; each connection is `egzo files connect` exec'd in it, which copies bytes between stdin/stdout and the server's
  socket. The server exits after five idle minutes and the container removes itself.
- Deleting a volume (`agent rm --worktrees`, `workspace rm`) first removes the helpers that mount it. `project up` and `project down`
  remove helpers like any container they do not want.

## The protocol of `egzo files serve`

One method, `GET` (and `HEAD`). The trailing slash says what is asked:

```
GET /notes/          a directory: {"entries": [{"name", "type": "dir"|"file"|"symlink", "size", "modified"}], "truncated": bool}
GET /notes/spec.pdf  a file: its bytes, Range supported, as application/octet-stream
```

- A directory without the slash redirects to it; a file with one is `404`.
- Entries are sorted by name, at most 10,000 (the first the directory gives, not the first by name); past that `truncated` is true.
- A symlink is listed and never followed or served. Pipes, sockets and devices are not listed, and refused.
- Every path is opened one part at a time from `/w`, each directory with `O_NOFOLLOW` and held open while the next part is looked up in
  it: nothing outside `/w` is reached, by `..` or by a link, and a link swapped in while a request runs is refused, not followed.
- A file is opened `O_RDONLY|O_NONBLOCK|O_NOFOLLOW` and checked to be regular after it is open.
- Any other method is `405`. Errors are a status and one line of text.
- The server has no write path: read-only is the absence of code, and the read-only mount under it.

## Who may (provisional)

Reading files is like reading the project's activity: `user` and above (`rbac.Files`). A viewer and an operator with no role see
nothing. Each download is written to the project's audit log. To be decided with the rest of the permissions: whether a `user` reads
the worktree of another person's agent (the worktree volume carries the `actor` label of its instance, so the hub can tell).

## Out of scope

Upload, delete, rename, edit, a file viewer, search, git (diffs, branches, commits), mounting from a desktop client. WebDAV was
considered and dropped: `golang.org/x/net/webdav` says it is not hardened against malicious clients, has no read-only mode, cannot turn
locks off, follows symlinks out of its tree and opens every file it lists; the two calls above are all the page needs.

## Order of work

1. Specs: `agent rm` keeps the worktree, `--worktrees` deletes it, `down` keeps them; the `workspaces` and `files` verbs through the
   fake hub (listing, a file, a range, a symlink, a pipe, a path outside, a method, a removed agent's worktree, a helper removed with
   its volume).
2. Core: `agent rm`/`down`, `internal/files` (server and relay), `egzo files serve|connect` (hidden), the helper in `stack`, the two
   verbs in `hublink` and `link`.
3. Hub: `links.Engine` (`Workspaces`, `DialFiles`), the routes, `rbac.Files`, the Files section, unit tests.
4. Docs: `workspaces.md`, `hub.md`, `DESIGN.md`.
