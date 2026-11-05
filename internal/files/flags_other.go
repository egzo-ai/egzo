// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

//go:build !unix

package files

import "os"

// openFlags: the server runs in a Linux container; elsewhere it only has to build.
const openFlags = os.O_RDONLY
