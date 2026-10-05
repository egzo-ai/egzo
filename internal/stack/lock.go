package stack

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock takes the project lock: one command at a time changes a project. Two `up` runs at once would
// both create the same containers, and a `down` would remove what an `up` is making. The lock is an
// advisory lock on .egzo/lock in the project directory, released when the process ends, even when it is
// killed. Commands that only read do not take it.
func Lock(dir string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Join(dir, ".egzo"), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, ".egzo", "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, fmt.Errorf("another egzo command is changing this project right now (%s is locked): wait for it to finish", filepath.Join(dir, ".egzo", "lock"))
		}
		return nil, err
	}
	return func() { syscall.Flock(int(file.Fd()), syscall.LOCK_UN); file.Close() }, nil
}
