// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// What an engine is (Podman or Docker, rootless or not) does not change while its socket stays the same file, and rootless Podman takes
// 350 ms to say it. A command is a new process, so the answer is kept in the user's cache directory, under a key that names the socket
// file itself: an engine that restarts makes a new one. Only a unix socket named by DOCKER_HOST is remembered; any problem with the
// cache means the engine is asked.

const rememberedCap = 16

// rememberKey names the socket behind host, or is "" when host is not a socket whose file can be told apart.
func rememberKey(host string) string {
	path, ok := strings.CutPrefix(host, "unix://")
	if !ok {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s|%d|%d.%d", host, stat.Ino, stat.Ctim.Sec, stat.Ctim.Nsec)
}

func rememberedFile() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "egzo", "engine-facts.json")
}

func recall(key, fact string) (value, found bool) {
	file := rememberedFile()
	if key == "" || file == "" {
		return false, false
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return false, false
	}
	var all map[string]map[string]bool
	if json.Unmarshal(data, &all) != nil {
		return false, false
	}
	value, found = all[key][fact]
	return value, found
}

func remember(key, fact string, value bool) {
	file := rememberedFile()
	if key == "" || file == "" {
		return
	}
	var all map[string]map[string]bool
	if data, err := os.ReadFile(file); err == nil && json.Unmarshal(data, &all) != nil {
		all = nil
	}
	if all == nil || len(all) >= rememberedCap && all[key] == nil {
		all = map[string]map[string]bool{}
	}
	if all[key] == nil {
		all[key] = map[string]bool{}
	}
	all[key][fact] = value
	data, err := json.Marshal(all)
	if err != nil || os.MkdirAll(filepath.Dir(file), 0o700) != nil {
		return
	}
	// a rename, so that a command reading at the same time sees the old file or the new one, never half of it
	temporary, err := os.CreateTemp(filepath.Dir(file), "engine-facts-*")
	if err != nil {
		return
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return
	}
	if temporary.Close() == nil {
		os.Rename(temporary.Name(), file)
	}
}
