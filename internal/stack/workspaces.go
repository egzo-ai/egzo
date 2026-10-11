// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/engine"
)

// WorkspaceRow is a workspace as `egzo workspace ls` shows it.
type WorkspaceRow struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`   // directory or git
	Volume    string   `json:"volume"` // the directory's volume, or the base clone's
	State     string   `json:"state"`  // a directory: exists or missing; a git workspace: cloned or not cloned
	Worktrees []string `json:"worktrees"`
}

// WorkspaceRows describes the declared workspaces of a project as the engine holds them.
func WorkspaceRows(project *config.Resolved, observed Observed) []WorkspaceRow {
	var rows []WorkspaceRow
	for _, name := range sortedKeys(project.Workspaces) {
		ws := project.Workspaces[name]
		row := WorkspaceRow{Name: name, Kind: "directory", Volume: WorkspaceVolume(project.Name, name), State: "missing", Worktrees: []string{}}
		if ws.Git != nil {
			row.Kind, row.Volume, row.State = "git", BaseVolume(project.Name, name), "not cloned"
		}
		if observed.find("volume", row.Volume) != nil {
			row.State = "exists"
			if ws.Git != nil {
				row.State = "cloned"
			}
		}
		for _, r := range observed.Resources {
			if r.Type == "volume" && r.Kind == kindWorkspace && r.Workspace == name && r.Instance != "" {
				row.Worktrees = append(row.Worktrees, r.Instance)
			}
		}
		sort.Strings(row.Worktrees)
		rows = append(rows, row)
	}
	return rows
}

// RemoveWorkspaces deletes the volumes of workspaces: the directory, or the base clone of a git workspace. It
// refuses while a container mounts the volume, and a git workspace while a worktree of it is left. With all it
// deletes every workspace volume the project has, left-over worktrees included, that nothing uses.
func RemoveWorkspaces(ctx context.Context, c *engine.Client, observed Observed, names []string, all bool, out io.Writer) error {
	owned := map[string][]Resource{} // workspace -> its base or directory volume, then its worktrees
	var order []string
	for _, r := range observed.Resources {
		if r.Type == "volume" && r.Kind == kindWorkspace && r.Workspace != "" {
			if _, seen := owned[r.Workspace]; !seen {
				order = append(order, r.Workspace)
			}
			owned[r.Workspace] = append(owned[r.Workspace], r)
		}
	}
	sort.Strings(order)
	if all {
		names = order
	}
	for _, name := range names {
		if len(owned[name]) == 0 {
			return fmt.Errorf("workspace %q has no volume in this project (workspaces with volumes: %s)", name, strings.Join(order, ", "))
		}
	}
	// refuse first, so nothing is removed unless everything may be
	for _, name := range names {
		for _, volume := range owned[name] {
			users, err := containersUsing(ctx, c, volume.Name)
			if err != nil {
				return err
			}
			if len(users) > 0 {
				return fmt.Errorf("volume %s is used by %s: remove those instances first (`egzo agent rm`)", volume.Name, strings.Join(users, ", "))
			}
			if !all && volume.Instance != "" {
				return fmt.Errorf("workspace %q still has the worktree volume %s of instance %q: "+
					"`egzo workspace rm %s --worktree %s` removes it", name, volume.Name, volume.Instance, name, volume.Instance)
			}
		}
	}
	for _, name := range names {
		for _, volume := range owned[name] {
			fmt.Fprintf(out, "remove volume %s\n", volume.Name)
			if err := c.API.VolumeRemove(ctx, volume.ID, true); err != nil {
				return fmt.Errorf("remove volume %s: %w", volume.Name, err)
			}
		}
	}
	return nil
}

// RemoveWorktreeVolumes deletes the worktree volumes that no instance owns any more (a spawn that could not undo
// itself, or an instance whose removal did not finish), and makes the base forget them. The branches stay.
func RemoveWorktreeVolumes(ctx context.Context, c *engine.Client, env PrepEnv, observed Observed, project, dir, workspace string, instances []string, out io.Writer) error {
	for _, instance := range instances {
		if _, ok := observed.Instance(instance); ok {
			return fmt.Errorf("instance %q exists: remove it with `egzo agent rm %s`, which removes its worktree too", instance, instance)
		}
		name := WorktreeVolume(project, workspace, instance)
		if observed.find("volume", name) == nil {
			return fmt.Errorf("workspace %q has no worktree volume for %q (%s)", workspace, instance, name)
		}
	}
	for _, instance := range instances {
		name := WorktreeVolume(project, workspace, instance)
		fmt.Fprintf(out, "remove volume %s\n", name)
		if err := c.API.VolumeRemove(ctx, name, true); err != nil {
			return fmt.Errorf("remove volume %s: %w", name, err)
		}
	}
	return ForgetWorktrees(ctx, c, env, project, dir, observed, instances)
}

// volumeHelper creates, and never starts, a container that mounts a volume, for the archive API to copy through:
// copying works on a created container's mounts, on any engine, local or remote. cleanup removes it.
func volumeHelper(ctx context.Context, c *engine.Client, env PrepEnv, project, volume string) (id string, cleanup func(), err error) {
	if err := ensureImage(ctx, c, env.Image); err != nil {
		return "", nil, err
	}
	identity := engine.Identity{Project: project, Service: "prep", Kind: "prep"}
	created, err := c.API.ContainerCreate(ctx,
		&container.Config{Image: env.Image, Cmd: []string{"/egzo", "version"}, Labels: identity.Labels()},
		&container.HostConfig{NetworkMode: "none", Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: volume, Target: "/w"}}},
		nil, nil, "")
	if err != nil {
		return "", nil, fmt.Errorf("prepare to copy through %s: %w", volume, err)
	}
	return created.ID, func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		c.API.ContainerRemove(cleanup, created.ID, container.RemoveOptions{Force: true})
	}, nil
}

// InsidePath cleans a path given as inside a workspace and refuses one that leaves it: absolute, or climbing out.
func InsidePath(p string) (string, error) {
	cleaned := path.Clean(filepath.ToSlash(p))
	if cleaned == "." || cleaned == "" {
		return "", nil
	}
	if path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%q is outside the workspace", p)
	}
	return cleaned, nil
}

func ownerOf(user string) (uid, gid int) {
	if u, g, ok := strings.Cut(user, ":"); ok {
		uid, _ = strconv.Atoi(u)
		gid, _ = strconv.Atoi(g)
	}
	return uid, gid
}

// PutFiles copies a file or a directory of this machine into a directory workspace, as the agent user. dest is the
// path in the workspace: for a directory it is the directory the contents go to ("" is the workspace's root).
func PutFiles(ctx context.Context, c *engine.Client, env PrepEnv, project, dir, workspace, src, dest string, out io.Writer) error {
	dest, err := InsidePath(dest)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	volume := WorkspaceVolume(project, workspace)
	if _, err := ensureOwnedVolume(ctx, c, env, volume, workspaceIdentity(project, dir, workspace, nil, "")); err != nil {
		return err
	}
	id, cleanup, err := volumeHelper(ctx, c, env, project, volume)
	if err != nil {
		return err
	}
	defer cleanup()

	reader, writer := io.Pipe()
	uid, gid := ownerOf(env.User)
	go func() { writer.CloseWithError(archiveTo(writer, src, info, dest, uid, gid)) }()
	copyErr := c.API.CopyToContainer(ctx, id, "/w", reader, container.CopyToContainerOptions{CopyUIDGID: true})
	reader.Close()
	if copyErr != nil {
		return fmt.Errorf("copy %s into workspace %q: %w", src, workspace, copyErr)
	}
	// the engine made the directories above dest as root
	if err := chownVolumes(ctx, c, env.Image, env.User, []string{volume}, engine.Identity{Project: project, ProjectDir: dir}); err != nil {
		return err
	}
	fmt.Fprintf(out, "put %s into workspace %s\n", src, workspace)
	return nil
}

// archiveTo writes src, a file or a directory, as a tar stream rooted at dest, owned by uid:gid.
func archiveTo(w io.Writer, src string, info fs.FileInfo, dest string, uid, gid int) error {
	archive := tar.NewWriter(w)
	defer archive.Close() // the trailer: a stricter reader than Go's needs it
	header := func(name string, info fs.FileInfo, link string) (*tar.Header, error) {
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return nil, err
		}
		h.Name, h.Uid, h.Gid, h.Uname, h.Gname = name, uid, gid, "", ""
		return h, nil
	}
	// the parents of dest, so that the user owns them
	var parents []string
	for parent := path.Dir(dest); parent != "." && parent != "/" && dest != ""; parent = path.Dir(parent) {
		parents = append([]string{parent}, parents...)
	}
	for _, parent := range parents {
		if err := archive.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: parent + "/", Mode: 0o755, Uid: uid, Gid: gid}); err != nil {
			return err
		}
	}
	if !info.IsDir() {
		name := dest
		if name == "" {
			name = filepath.Base(src)
		}
		if err := writeEntry(archive, header, src, name, info); err != nil {
			return err
		}
		return archive.Close()
	}
	err := filepath.WalkDir(src, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		name := path.Join(dest, filepath.ToSlash(rel))
		if rel == "." && dest == "" {
			return nil // the workspace's root exists
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		return writeEntry(archive, header, p, name, entryInfo)
	})
	if err != nil {
		return err
	}
	return archive.Close()
}

func writeEntry(archive *tar.Writer, header func(string, fs.FileInfo, string) (*tar.Header, error), file, name string, info fs.FileInfo) error {
	link := ""
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(file)
		if err != nil {
			return err
		}
		link = target
	}
	if !info.Mode().IsRegular() && !info.IsDir() && link == "" {
		return nil // sockets, devices and the like are not files of a workspace
	}
	h, err := header(name, info, link)
	if err != nil {
		return err
	}
	if info.IsDir() {
		h.Name += "/"
	}
	if err := archive.WriteHeader(h); err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(archive, f)
	return err
}

// GetFiles copies a path of a workspace volume (a directory workspace's, or an instance's worktree) out to this
// machine. A directory's contents go to dest, which is made; a file goes to dest, or into it when dest is a directory.
func GetFiles(ctx context.Context, c *engine.Client, env PrepEnv, project, volume, src, dest string, out io.Writer) error {
	src, err := InsidePath(src)
	if err != nil {
		return err
	}
	exists, err := volumeExists(ctx, c, volume)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("the volume %s does not exist", volume)
	}
	id, cleanup, err := volumeHelper(ctx, c, env, project, volume)
	if err != nil {
		return err
	}
	defer cleanup()
	source := "/w"
	if src != "" {
		source += "/" + src
	}
	stat, err := c.API.ContainerStatPath(ctx, id, source)
	if err != nil {
		return fmt.Errorf("copy %q out of %s: %w", displayPath(src), volume, err)
	}
	copying := source
	if stat.Mode.IsDir() {
		copying += "/." // the contents of the directory, not the directory
	}
	reader, _, err := c.API.CopyFromContainer(ctx, id, copying)
	if err != nil {
		return fmt.Errorf("copy %q out of %s: %w", displayPath(src), volume, err)
	}
	defer reader.Close()
	if err := extractTo(reader, dest, stat.Mode.IsDir()); err != nil {
		return err
	}
	fmt.Fprintf(out, "got %s from %s into %s\n", displayPath(src), volume, dest)
	return nil
}

func displayPath(p string) string {
	if p == "" {
		return "."
	}
	return p
}

// extractTo unpacks a tar stream of a directory's contents into dest, or a stream of one file into dest. Nothing
// leaves dest: an entry that would is an error, and a link that points out is not made.
func extractTo(r io.Reader, dest string, directory bool) error {
	archive := tar.NewReader(r)
	if directory {
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
	}
	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	for {
		h, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(path.Clean(h.Name), "./")
		if name == "." || name == "" {
			continue
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if !directory {
			target = root
			if info, err := os.Stat(root); err == nil && info.IsDir() {
				target = filepath.Join(root, path.Base(name))
			}
		}
		if rel, err := filepath.Rel(root, target); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("the archive holds %q, which is outside %s", h.Name, dest)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, 0o755)
		case tar.TypeSymlink:
			if linkLeaves(name, h.Linkname) {
				fmt.Fprintf(os.Stderr, "warning: not copying the link %s -> %s: it leads out of the copy\n", name, h.Linkname)
				continue
			}
			os.Remove(target)
			err = os.Symlink(h.Linkname, target)
		case tar.TypeReg:
			if err = os.MkdirAll(filepath.Dir(target), 0o755); err == nil {
				err = writeFile(target, archive, os.FileMode(h.Mode)&0o777)
			}
		}
		if err != nil {
			return err
		}
	}
}

func writeFile(target string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode|0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// linkLeaves says whether a symbolic link at name (relative to the copy's root) points outside the root.
func linkLeaves(name, target string) bool {
	if path.IsAbs(target) {
		return true
	}
	resolved := path.Join(path.Dir(name), target)
	return resolved == ".." || strings.HasPrefix(resolved, "../")
}
