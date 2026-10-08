// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Lock takes the project lock: one command at a time changes a project. Two `up` runs at once would
// both create the same containers, and a `down` would remove what an `up` is making. The lock is an
// advisory lock on .egzo/lock in the project directory, released when the process ends, even when it is
// killed. Commands that only read do not take it.
func Lock(dir string) (release func(), err error) { return lock(dir, 0) }

// LockWait is Lock for commands that are meant to run side by side, such as several spawns at once: when
// another command holds the lock it waits for it, up to the timeout, and then fails like Lock.
func LockWait(dir string, timeout time.Duration) (release func(), err error) {
	return lock(dir, timeout)
}

func lock(dir string, wait time.Duration) (release func(), err error) {
	if err := os.MkdirAll(filepath.Join(dir, ".egzo"), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, ".egzo", "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err != syscall.EWOULDBLOCK || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		file.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, fmt.Errorf("another egzo command is changing this project right now (%s is locked): wait for it to finish", filepath.Join(dir, ".egzo", "lock"))
		}
		return nil, err
	}
	return func() { syscall.Flock(int(file.Fd()), syscall.LOCK_UN); file.Close() }, nil
}
