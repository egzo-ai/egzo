// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

//go:build unix

package files

import (
	"os"
	"syscall"
)

// openFlags open a file to read without waiting: a pipe swapped in for a file must not hold the connection.
const openFlags = os.O_RDONLY | syscall.O_NONBLOCK
