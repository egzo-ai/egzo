// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"

	"github.com/egzo-ai/egzo/internal/engine"
)

// An agent runs as the uid of the person who started the project, and an image does not know that uid. Programs that
// ask who they are (ssh, whoami, git, python's getpass) fail or print a bare number, and ssh takes ~/.ssh from the home in
// the passwd entry, not from $HOME. egzo gives the user an entry before the container starts, in the container's own
// layer: through the engine's API, so it works against an engine on another machine, and for any image.

const (
	passwdFile = "/etc/passwd"
	groupFile  = "/etc/group"
	// agentAccount is the name of the entry egzo adds. An image that has no entry for the uid has no use for the name.
	agentAccount = "agent"
)

// ensureUserEntry makes sure the user the container will run as has a passwd entry whose home is the HOME the container is
// given, and that its group exists. The container is created and not started.
func ensureUserEntry(ctx context.Context, c *engine.Client, id string, env []string) error {
	inspected, err := c.API.ContainerInspect(ctx, id)
	if err != nil {
		return err
	}
	passwd, found, err := readContainerFile(ctx, c, id, passwdFile)
	if err != nil || !found {
		return err // an image with no passwd file has no program that reads one
	}
	group, _, err := readContainerFile(ctx, c, id, groupFile)
	if err != nil {
		return err
	}
	passwd, group, changed := withUserEntry(passwd, group, inspected.Config.User, envValue(env, "HOME"))
	if !changed {
		return nil
	}
	return writeContainerFiles(ctx, c, id, map[string][]byte{passwdFile: passwd, groupFile: group})
}

func envValue(env []string, key string) string {
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, key+"="); ok {
			return value
		}
	}
	return ""
}

// withUserEntry returns the passwd and group files with an entry for user (as the engine takes it: "", "uid", "uid:gid",
// "name" or "name:group"). A user the files do not know is added as "agent" with home as its home ("/" when there is none).
// A user they know keeps its entry, except that its home becomes home when one is given: the home egzo mounts is the
// agent's home, whatever the image thought. changed says whether either file differs.
func withUserEntry(passwd, group []byte, user, home string) (newPasswd, newGroup []byte, changed bool) {
	userPart, groupPart, _ := strings.Cut(user, ":")
	passwdLines, groupLines := splitLines(passwd), splitLines(group)

	uid, known := 0, true
	index := -1
	if userPart != "" {
		if n, err := strconv.Atoi(userPart); err == nil {
			uid = n
			index = findEntry(passwdLines, 2, userPart)
		} else {
			index = findEntry(passwdLines, 0, userPart)
			known = index >= 0
			if known {
				uid, _ = strconv.Atoi(strings.Split(passwdLines[index], ":")[2])
			}
		}
	} else {
		index = findEntry(passwdLines, 2, "0")
	}
	if !known {
		return passwd, group, false // a name the image does not have: the engine refuses to start it, and says so
	}

	gid := 0
	switch {
	case groupPart != "":
		if n, err := strconv.Atoi(groupPart); err == nil {
			gid = n
		} else if at := findEntry(groupLines, 0, groupPart); at >= 0 {
			gid, _ = strconv.Atoi(strings.Split(groupLines[at], ":")[2])
		}
	case index >= 0:
		gid, _ = strconv.Atoi(strings.Split(passwdLines[index], ":")[3])
	}

	if index < 0 {
		name := agentAccount
		if findEntry(passwdLines, 0, name) >= 0 {
			name = fmt.Sprintf("%s%d", agentAccount, uid)
		}
		if home == "" {
			home = "/"
		}
		passwdLines = append(passwdLines, fmt.Sprintf("%s:x:%d:%d:%s:%s:/bin/sh", name, uid, gid, name, home))
		changed = true
	} else if fields := strings.Split(passwdLines[index], ":"); home != "" && len(fields) == 7 && fields[5] != home {
		fields[5] = home
		passwdLines[index] = strings.Join(fields, ":")
		changed = true
	}

	if findEntry(groupLines, 2, strconv.Itoa(gid)) < 0 {
		name := agentAccount
		if findEntry(groupLines, 0, name) >= 0 {
			name = fmt.Sprintf("%s%d", agentAccount, gid)
		}
		groupLines = append(groupLines, fmt.Sprintf("%s:x:%d:", name, gid))
		changed = true
	}
	if !changed {
		return passwd, group, false
	}
	return joinLines(passwdLines), joinLines(groupLines), true
}

// findEntry is the index of the first line of a passwd or group file whose colon-separated field is value, or -1.
func findEntry(lines []string, field int, value string) int {
	for i, line := range lines {
		if fields := strings.Split(line, ":"); len(fields) > field && fields[field] == value && !strings.HasPrefix(line, "#") {
			return i
		}
	}
	return -1
}

func splitLines(data []byte) []string {
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func joinLines(lines []string) []byte { return []byte(strings.Join(lines, "\n") + "\n") }

// readContainerFile reads one regular file of a container's filesystem, which need not be running.
func readContainerFile(ctx context.Context, c *engine.Client, id, path string) (data []byte, found bool, err error) {
	reader, _, err := c.API.CopyFromContainer(ctx, id, path)
	if err != nil {
		if engine.IsNoSuchContainer(err) || strings.Contains(err.Error(), "Could not find the file") || strings.Contains(err.Error(), "no such file") {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read %s of the container: %w", path, err)
	}
	defer reader.Close()
	archive := tar.NewReader(reader)
	if _, err := archive.Next(); err != nil {
		return nil, false, fmt.Errorf("read %s of the container: %w", path, err)
	}
	data, err = io.ReadAll(archive)
	return data, err == nil, err
}

// writeContainerFiles puts files (by absolute path, all under /etc) in a container's own layer, owned by root and
// readable by everyone.
func writeContainerFiles(ctx context.Context, c *engine.Client, id string, files map[string][]byte) error {
	var buffer bytes.Buffer
	archive := tar.NewWriter(&buffer)
	for _, path := range []string{passwdFile, groupFile} {
		content, ok := files[path]
		if !ok {
			continue
		}
		header := &tar.Header{Name: strings.TrimPrefix(path, "/etc/"), Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if _, err := archive.Write(content); err != nil {
			return err
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if err := c.API.CopyToContainer(ctx, id, "/etc", &buffer, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("give the user of the container a passwd entry: %w", err)
	}
	return nil
}
