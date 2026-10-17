# OpenCode's TUI does not start under gVisor

Status: **open, gVisor or OpenCode, not egzo** (found 2026-10-17, runsc release-20260921 and 20261012, OpenCode 1.18.35).

egzo runs a harness's TUI on a pty it creates inside the container (the session holder, `egzo agent run`). Under gVisor
(`docker-gvisor`, with `--network=host`, see [gvisor-docker-embedded-dns.md](gvisor-docker-embedded-dns.md)) Claude Code starts and
works, and OpenCode does not: the process runs, writes a burst of terminal palette queries (`OSC 4;n;?`, `OSC 10..19;?`), and draws nothing
more. The spec `test_the_real_tui_reports_idle…[opencode]` times out waiting for "Ask anything".

Narrowed down without egzo, in a plain `runsc` container with the OpenCode image:

| how OpenCode is started | result |
|---|---|
| `docker exec -it … opencode` (the pty is made by the engine) | draws the prompt in about a second |
| `docker exec -it … script -qfc opencode /dev/null` (a pty made inside the sandbox) | 205 bytes, no prompt, even when every palette query is answered |
| the same two under runc | both draw the prompt |

So it is the pty made inside the sandbox, which is what egzo and any terminal multiplexer do. No upstream report turned up in a web search (2026-10-17, the trackers themselves were not searched); worth a report to google/gvisor with the `script` reproduction. The two specs are strict xfail on the
gVisor platform until it is fixed.
