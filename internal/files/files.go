// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// Package files serves the files of one workspace, read only, to the hub: `egzo files serve` runs it in a helper container that mounts
// the workspace's volume read only at /w, and the project's link carries each connection to it with `egzo files connect`.
//
// It speaks two answers to one method. GET of a path that ends with a slash lists a directory as JSON; GET of any other path sends a
// regular file's bytes. There is no way to write, and every path is opened one part at a time from the root without following a link, so
// nothing outside the root is reached, by `..` or by a symbolic link, even while the files change: they are written by an agent, and are
// not trusted.
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
	"syscall"
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

// Handler serves the files under Root, a directory held open.
type Handler struct {
	Root *os.File
	// Max is the most entries a listing holds; 0 is MaxEntries.
	Max int
}

// errLink is a symbolic link met on the way: it is never followed.
var errLink = errors.New("a symbolic link is not followed")

// errOutside is a path that would leave the root.
var errOutside = errors.New("outside the workspace")

// stat is what an entry is, read without following a link.
type stat struct {
	mode     fs.FileMode // only the type
	size     int64
	modified time.Time
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
	// the directory that holds name is held open, reached without following a link: what is read next is read in it
	dir, base, err := walk(h.Root, name)
	if err != nil {
		missing(w, err)
		return
	}
	defer dir.Close()
	info, err := statAt(dir, base)
	if err != nil {
		missing(w, err)
		return
	}
	switch mode := info.mode; {
	case mode.IsDir():
		if !wantDir {
			// relative, so it holds wherever the files are served from
			w.Header().Set("Location", path.Base(asked)+"/")
			w.WriteHeader(http.StatusMovedPermanently)
			return
		}
		h.list(w, r, dir, base)
	case mode&fs.ModeSymlink != 0:
		fail(w, http.StatusForbidden, errLink.Error())
	case wantDir:
		fail(w, http.StatusNotFound, "not a directory")
	case mode.IsRegular():
		h.send(w, r, dir, base)
	default:
		fail(w, http.StatusForbidden, "not a regular file")
	}
}

func (h Handler) list(w http.ResponseWriter, r *http.Request, parent *os.File, name string) {
	dir, err := openAt(parent, name, true)
	if err != nil {
		missing(w, err)
		return
	}
	defer dir.Close()
	max := h.Max
	if max <= 0 {
		max = MaxEntries
	}
	// ReadDir reads the names without opening them, and each is looked at in this directory without following it: a pipe blocks nothing
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
		info, err := statAt(dir, entry.Name())
		if err != nil {
			continue // gone since
		}
		e := Entry{Name: entry.Name(), Modified: info.modified.UTC()}
		switch mode := info.mode; {
		case mode.IsDir():
			e.Type = TypeDir
		case mode&fs.ModeSymlink != 0:
			e.Type = TypeSymlink
		case mode.IsRegular():
			e.Type, e.Size = TypeFile, info.size
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

func (h Handler) send(w http.ResponseWriter, r *http.Request, dir *os.File, name string) {
	f, err := openAt(dir, name, false)
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
	case errors.Is(err, errLink):
		fail(w, http.StatusForbidden, errLink.Error())
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		fail(w, http.StatusNotFound, "no such file or directory")
	case errors.Is(err, fs.ErrPermission):
		fail(w, http.StatusForbidden, "permission denied")
	default:
		fail(w, http.StatusForbidden, "outside the workspace, or not readable")
	}
}

func fail(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	fmt.Fprintln(w, text)
}
