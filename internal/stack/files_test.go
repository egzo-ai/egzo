// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func readArchive(t *testing.T, data []byte) map[string]*tar.Header {
	t.Helper()
	found := map[string]*tar.Header{}
	reader := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := reader.Next()
		if err == io.EOF {
			return found
		}
		if err != nil {
			t.Fatalf("the archive is not a whole tar stream: %v", err)
		}
		found[h.Name] = h
	}
}

func TestArchiveOfAFileIsAWholeStreamOwnedByTheAgentUser(t *testing.T) {
	src := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(src, []byte("alpha\n"), 0o640)
	info, _ := os.Stat(src)
	var out bytes.Buffer
	if err := archiveTo(&out, src, info, "notes/a.txt", 1234, 5678); err != nil {
		t.Fatal(err)
	}
	found := readArchive(t, out.Bytes())
	file, parent := found["notes/a.txt"], found["notes/"]
	if file == nil || parent == nil || file.Uid != 1234 || file.Gid != 5678 || parent.Uid != 1234 || file.Size != 6 {
		t.Errorf("archive = %+v", found)
	}
}

func TestArchiveOfADirectoryHasItsContentsUnderDestAndKeepsLinks(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "sub"), 0o755)
	os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("beta"), 0o644)
	os.Symlink("sub/b.txt", filepath.Join(src, "link"))
	info, _ := os.Stat(src)
	var out bytes.Buffer
	if err := archiveTo(&out, src, info, "docs", 1, 2); err != nil {
		t.Fatal(err)
	}
	found := readArchive(t, out.Bytes())
	for _, name := range []string{"docs/", "docs/sub/", "docs/sub/b.txt", "docs/link"} {
		if found[name] == nil {
			t.Errorf("%s is not in the archive: %v", name, found)
		}
	}
	if link := found["docs/link"]; link == nil || link.Typeflag != tar.TypeSymlink || link.Linkname != "sub/b.txt" {
		t.Errorf("link = %+v", link)
	}
	var root bytes.Buffer
	if err := archiveTo(&root, src, info, "", 1, 2); err != nil {
		t.Fatal(err)
	}
	if found := readArchive(t, root.Bytes()); found["sub/b.txt"] == nil || found["./"] != nil {
		t.Errorf("dest \"\" puts the contents in the root: %v", found)
	}
}

func tarOf(t *testing.T, entries ...tar.Header) io.Reader {
	t.Helper()
	var out bytes.Buffer
	writer := tar.NewWriter(&out)
	for _, h := range entries {
		h := h
		if err := writer.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			writer.Write(make([]byte, h.Size))
		}
	}
	writer.Close()
	return &out
}

func TestExtractNeverLeavesTheDestination(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "out")
	err := extractTo(tarOf(t, tar.Header{Typeflag: tar.TypeReg, Name: "../escape.txt", Size: 1, Mode: 0o644}), dest, true)
	if err == nil {
		t.Fatal("an entry that climbs out was accepted")
	}
	if _, statErr := os.Stat(filepath.Join(root, "escape.txt")); statErr == nil {
		t.Fatal("a file was written outside the destination")
	}
}

func TestExtractKeepsLinksThatStayInsideAndSkipsThoseThatLeave(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out")
	err := extractTo(tarOf(t,
		tar.Header{Typeflag: tar.TypeDir, Name: "pkg/", Mode: 0o755},
		tar.Header{Typeflag: tar.TypeReg, Name: "pkg/bin", Size: 2, Mode: 0o755},
		tar.Header{Typeflag: tar.TypeDir, Name: "node_modules/.bin/", Mode: 0o755},
		tar.Header{Typeflag: tar.TypeSymlink, Name: "node_modules/.bin/x", Linkname: "../../pkg/bin"},
		tar.Header{Typeflag: tar.TypeSymlink, Name: "evil", Linkname: "/etc/passwd"},
		tar.Header{Typeflag: tar.TypeSymlink, Name: "up", Linkname: "../outside"},
	), dest, true)
	if err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(dest, "node_modules", ".bin", "x")); err != nil || target != "../../pkg/bin" {
		t.Errorf("a relative link inside the copy was dropped: %q, %v", target, err)
	}
	for _, name := range []string{"evil", "up"} {
		if _, err := os.Lstat(filepath.Join(dest, name)); err == nil {
			t.Errorf("the link %s that leaves the copy was made", name)
		}
	}
}

func TestExtractOfAFileGoesToDestOrIntoADirectoryDest(t *testing.T) {
	file := func() io.Reader {
		return tarOf(t, tar.Header{Typeflag: tar.TypeReg, Name: "README", Size: 3, Mode: 0o644})
	}
	root := t.TempDir()
	if err := extractTo(file(), filepath.Join(root, "copy.txt"), false); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(root, "copy.txt")); err != nil || info.Size() != 3 {
		t.Errorf("file dest: %v", err)
	}
	if err := extractTo(file(), root, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "README")); err != nil {
		t.Errorf("directory dest: %v", err)
	}
}

func TestLinkLeavesIsJudgedFromTheLinksOwnDirectory(t *testing.T) {
	for _, c := range []struct {
		name, target string
		leaves       bool
	}{
		{"a/b", "../c", false}, {"a/b", "../../c", true}, {"b", "../c", true}, {"a/b", "/etc", true}, {"a/b", "c/d", false},
	} {
		if got := linkLeaves(c.name, c.target); got != c.leaves {
			t.Errorf("%s -> %s: leaves = %v", c.name, c.target, got)
		}
	}
}

func TestOwnerOfReadsUidAndGid(t *testing.T) {
	if uid, gid := ownerOf("1000:1001"); uid != 1000 || gid != 1001 {
		t.Errorf("%d:%d", uid, gid)
	}
	if uid, gid := ownerOf(""); uid != 0 || gid != 0 {
		t.Errorf("empty = %d:%d", uid, gid)
	}
}
