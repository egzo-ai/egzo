// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import "syscall"

// backgroundAttr makes a plan's Background command die with `egzo agent run`, even when that one is killed.
func backgroundAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM} }
