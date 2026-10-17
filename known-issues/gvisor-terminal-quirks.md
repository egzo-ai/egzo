# Terminals under gVisor: OpenCode's TUI, Ctrl-D, and `tty`

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

## Ctrl-D at the start of a line does not end a program's input

A program on a pty that a Go program made (egzo's session holder, `creack/pty`) does not see end of input when `^D` is written to
the master at the start of a line. Reproduced without egzo with a 50-line Go program that starts `sh -c 'while read l; …; echo eof-seen'`
on a pty and writes `\x04`: `eof-seen` appears under runc and not under runsc, through an `os.File` write and through a blocking
`write(2)`. `script` (a C `openpty`) delivers it under runsc, so it depends on how the pty is made. The spec
`test_end_of_input_typed_at_the_start_of_a_line_reaches_the_program` is a strict xfail on the gVisor platform.

## `tty` says "not a tty" in `docker exec -it`

Not a bug of egzo, and not a failure: under gVisor the terminal of an exec is a host file descriptor, not a `/dev/pts/N` entry
(`ls -l /proc/self/fd/0` shows `host:[4]`), so `tty` finds no name while `[ -t 0 ]` is true and `stty size` works. The spec
`test_exec_with_a_terminal_…` tests `[ -t 0 ]`, which says what it means on every engine.

## Files copied with `docker cp` are not there

gVisor keeps its own view of the container's root filesystem: a file `docker cp` writes into a running gVisor container is not visible
inside it. The specs that copied a helper script (`browser`, `proxy` isolation) now write it through the container's own shell
(`Engine.put_file`). egzo itself copies only into engine volumes (`workspace put`), which gVisor does see, and those specs pass.
