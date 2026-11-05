// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package files serves the files of one workspace, read only, to the hub: `egzo files serve` runs it in a helper container that mounts
// the workspace's volume read only at /w, and the project's link carries each connection to it with `egzo files connect`.
//
// It speaks two answers to one method. GET of a path that ends with a slash lists a directory as JSON; GET of any other path sends a
// regular file's bytes. There is no way to write, and every path is opened through an os.Root, so nothing outside the root is reached, by
// `..` or by a symbolic link: the files are written by an agent, and are not trusted.
package files

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// MaxEntries is the most a listing holds. A directory with more is cut, and says so.
const MaxEntries = 10000

// The types of an entry. Anything else (a pipe, a socket, a device) is not listed: it is no file to download.
const (
	TypeDir     = "dir"
	TypeFile    = "file"
	TypeSymlink = "symlink"
)

// Entry is one entry of a directory.
type Entry struct {
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Size     int64     `json:"size,omitempty"` // a file's
	Modified time.Time `json:"modified"`
}

// Listing is what a directory holds, sorted by name.
type Listing struct {
	Entries []Entry `json:"entries"`
	// Truncated says the directory holds more than MaxEntries entries, and the listing only some of them.
	Truncated bool `json:"truncated"`
}

// Handler serves the files under root.
type Handler struct {
	Root *os.Root
	// Max is the most entries a listing holds; 0 is MaxEntries.
	Max int
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		fail(w, http.StatusMethodNotAllowed, "only GET and HEAD: the files are read only")
		return
	}
	asked := r.URL.Path
	if !strings.HasPrefix(asked, "/") || strings.ContainsRune(asked, 0) {
		fail(w, http.StatusBadRequest, "not a path")
		return
	}
	wantDir := strings.HasSuffix(asked, "/")
	name := strings.TrimPrefix(path.Clean(asked), "/")
	if name == "" {
		name = "."
	}
	// os.Root keeps a link inside the root, and still follows one; a link is never followed here, in any part of the path
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		info, err := h.Root.Lstat(parent)
		if err != nil {
			missing(w, err)
			return
		}
		if !info.IsDir() {
			fail(w, http.StatusForbidden, "a symbolic link is not followed")
			return
		}
	}
	info, err := h.Root.Lstat(name)
	if err != nil {
		missing(w, err)
		return
	}
	switch mode := info.Mode(); {
	case mode.IsDir():
		if !wantDir {
			// relative, so it holds wherever the files are served from
			w.Header().Set("Location", path.Base(asked)+"/")
			w.WriteHeader(http.StatusMovedPermanently)
			return
		}
		h.list(w, r, name)
	case wantDir:
		fail(w, http.StatusNotFound, "not a directory")
	case mode&fs.ModeSymlink != 0:
		fail(w, http.StatusForbidden, "a symbolic link is not followed")
	case mode.IsRegular():
		h.send(w, r, name)
	default:
		fail(w, http.StatusForbidden, "not a regular file")
	}
}

func (h Handler) list(w http.ResponseWriter, r *http.Request, name string) {
	dir, err := h.Root.Open(name)
	if err != nil {
		missing(w, err)
		return
	}
	defer dir.Close()
	max := h.Max
	if max <= 0 {
		max = MaxEntries
	}
	// ReadDir reads the entries' types without opening them, and Info lstats: a pipe in the directory blocks nothing
	found, err := dir.ReadDir(max + 1)
	if err != nil && len(found) == 0 && !errors.Is(err, io.EOF) {
		fail(w, http.StatusInternalServerError, "the directory cannot be read")
		return
	}
	listing := Listing{Entries: []Entry{}, Truncated: len(found) > max}
	if listing.Truncated {
		found = found[:max]
	}
	for _, entry := range found {
		info, err := entry.Info()
		if err != nil {
			continue // gone since
		}
		e := Entry{Name: entry.Name(), Modified: info.ModTime().UTC()}
		switch mode := info.Mode(); {
		case mode.IsDir():
			e.Type = TypeDir
		case mode&fs.ModeSymlink != 0:
			e.Type = TypeSymlink
		case mode.IsRegular():
			e.Type, e.Size = TypeFile, info.Size()
		default:
			continue
		}
		listing.Entries = append(listing.Entries, e)
	}
	sort.Slice(listing.Entries, func(i, j int) bool { return listing.Entries[i].Name < listing.Entries[j].Name })
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(listing)
}

func (h Handler) send(w http.ResponseWriter, r *http.Request, name string) {
	// O_NONBLOCK: what was a file when it was looked at may be a pipe by now, and opening a pipe waits for a writer
	f, err := h.Root.OpenFile(name, openFlags, 0)
	if err != nil {
		missing(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		fail(w, http.StatusForbidden, "not a regular file")
		return
	}
	// what the file holds is never guessed at: it is bytes, to save
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

func missing(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		fail(w, http.StatusNotFound, "no such file or directory")
	case errors.Is(err, fs.ErrPermission):
		fail(w, http.StatusForbidden, "permission denied")
	default:
		// os.Root says a path that leaves the root, by .. or a link, is an error of its own
		fail(w, http.StatusForbidden, "outside the workspace, or not readable")
	}
}

func fail(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	fmt.Fprintln(w, text)
}
