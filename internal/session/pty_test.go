// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package session

import (
	"os"

	"github.com/creack/pty"
)

func ptyPair() (*os.File, *os.File, error) { return pty.Open() }
