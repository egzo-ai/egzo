// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

//go:build unix

package files

import (
	"io/fs"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// walk opens the directory that holds name, one part of the path at a time from root, each with O_NOFOLLOW: a link on the way is refused,
// never followed, and what is looked up next is looked up in the directory that was opened, whatever is renamed or swapped meanwhile. It
// returns that directory and the last part of name, which is not opened.
func walk(root *os.File, name string) (*os.File, string, error) {
	fd, err := unix.Openat(int(root.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", &fs.PathError{Op: "open", Path: name, Err: err}
	}
	parts := strings.Split(name, "/")
	for _, part := range parts {
		if part == "" || part == ".." {
			unix.Close(fd)
			return nil, "", &fs.PathError{Op: "open", Path: name, Err: errOutside}
		}
	}
	for _, part := range parts[:len(parts)-1] {
		if part == "." {
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstatat(fd, part, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			unix.Close(fd)
			return nil, "", &fs.PathError{Op: "open", Path: name, Err: err}
		}
		if st.Mode&unix.S_IFMT == unix.S_IFLNK {
			unix.Close(fd)
			return nil, "", &fs.PathError{Op: "open", Path: name, Err: errLink}
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return nil, "", &fs.PathError{Op: "open", Path: name, Err: err}
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), name), parts[len(parts)-1], nil
}

// statAt is what name in dir is, the link itself and not what it points at.
func statAt(dir *os.File, name string) (stat, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return stat{}, &fs.PathError{Op: "lstat", Path: name, Err: err}
	}
	s := stat{size: st.Size, modified: time.Unix(st.Mtim.Unix())}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		s.mode = fs.ModeDir
	case unix.S_IFLNK:
		s.mode = fs.ModeSymlink
	case unix.S_IFREG:
	default:
		s.mode = fs.ModeIrregular
	}
	return s, nil
}

// openAt opens name in dir to read, never following a link: a link swapped in since it was looked at fails to open. O_NONBLOCK: what was a
// file when it was looked at may be a pipe by now, and opening a pipe waits for a writer.
func openAt(dir *os.File, name string, directory bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_NONBLOCK | unix.O_NOFOLLOW | unix.O_NOCTTY | unix.O_CLOEXEC
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(int(dir.Fd()), name, flags, 0)
	if err != nil {
		if err == unix.ELOOP {
			err = errLink
		}
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return os.NewFile(uintptr(fd), name), nil
}
