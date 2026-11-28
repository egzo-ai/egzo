// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

//go:build !linux

package cli

import "syscall"

// backgroundAttr: `egzo agent run` runs in a Linux container; elsewhere there is nothing to set.
func backgroundAttr() *syscall.SysProcAttr { return nil }
