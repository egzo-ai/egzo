// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

//go:build !unix

package files

import (
	"errors"
	"os"
)

// The server runs in a Linux container; elsewhere it only has to build.

var errUnsupported = errors.New("files are served on unix only")

func walk(root *os.File, name string) (*os.File, string, error) { return nil, "", errUnsupported }

func statAt(dir *os.File, name string) (stat, error) { return stat{}, errUnsupported }

func openAt(dir *os.File, name string, directory bool) (*os.File, error) { return nil, errUnsupported }
