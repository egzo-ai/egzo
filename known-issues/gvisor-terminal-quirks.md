# Terminals under gVisor: Ctrl-D, `tty`, and OpenCode on older releases

Status: **Ctrl-D open** (gVisor, not egzo; found 2026-10-17, runsc release-20261012). The rest is explained below.

egzo runs a harness's TUI on a pty it creates inside the container (the session holder, `egzo agent run`).

## OpenCode's TUI did not start on runsc release-20260921

On release-20260921 OpenCode wrote a burst of terminal palette queries and drew nothing more, on any pty made inside the sandbox (even
`script -qfc opencode /dev/null`); on a pty made by the engine (`docker exec -it`) it drew. On **release-20261012** it draws on both, and
the specs pass. So a gVisor older than that cannot run OpenCode under egzo; Claude Code worked on both. Nothing is marked in the specs: the
CI installs the latest release.

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
