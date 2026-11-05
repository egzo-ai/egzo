// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/hub/internal/hub"
	"github.com/egzo-ai/egzo/hub/internal/store"
	"github.com/egzo-ai/egzo/hub/internal/textview"
	"github.com/egzo-ai/egzo/internal/names"
	"github.com/egzo-ai/egzo/internal/termsafe"
)

// The files of a project's workspaces (roadmap/hub-files.md): the hub lists the workspace volumes, and carries GET requests to the read-only
// file server of one of them over the project's link. What comes back was written by an agent: a file is always a download, never a page of
// the hub's origin.

// filesConnsPerVolume bounds the connections to one volume's file server; a request over it waits for one to be free.
const filesConnsPerVolume = 8

// filesRequestHeaders are the only headers a request takes to the file server.
var filesRequestHeaders = []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"}

// filesResponseHeaders are the only headers of the file server's answer that reach the browser.
var filesResponseHeaders = []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"}

type workspaceJSON struct {
	Name      string         `json:"name"`
	Kind      string         `json:"kind"`
	Worktrees []worktreeJSON `json:"worktrees"`
	Remote    string         `json:"remote,omitempty"` // a git workspace's, shown as text and never as a link
	Branch    string         `json:"branch,omitempty"`
}

type worktreeJSON struct {
	Instance string `json:"instance"`
	State    string `json:"state"`
	Creator  string `json:"creator"`
}

func (s *Server) workspaces(w http.ResponseWriter, r *http.Request, user store.User) {
	list, err := s.hub.Workspaces(r.Context(), user, r.PathValue("project"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	n := s.namer(r.Context())
	out := make([]workspaceJSON, 0, len(list))
	for _, ws := range list {
		row := workspaceJSON{Name: ws.Name, Kind: ws.Kind, Worktrees: []worktreeJSON{}}
		if ws.Kind == "git" {
			row.Remote, row.Branch = shownRemote(ws.Remote), termsafe.Truncate(termsafe.Line(ws.Branch), 200)
		}
		for _, t := range ws.Worktrees {
			row.Worktrees = append(row.Worktrees, worktreeJSON{Instance: t.Instance, State: t.State, Creator: n.actor(t.Actor)})
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": out})
}

// shownRemote is a remote as a link said it, if it is what a link says: an https URL with nothing that could be a secret. Anything else is
// not shown.
func shownRemote(remote string) string {
	u, err := url.Parse(remote)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(remote) > 2000 {
		return ""
	}
	return termsafe.Line(remote)
}

// filesTarget is the volume a request is for.
type filesTarget struct {
	project, workspace, instance string
}

type filesRequest struct{}

// filesState is what the server keeps for the file proxy.
type filesState struct {
	once  sync.Once
	proxy *httputil.ReverseProxy
}

// files serves a file or a directory listing of a workspace volume: a directory workspace's, or with {instance} a worktree's.
func (s *Server) files(w http.ResponseWriter, r *http.Request, user store.User) {
	target := filesTarget{project: r.PathValue("project"), workspace: r.PathValue("workspace"), instance: r.PathValue("instance")}
	if !names.Identifier(target.workspace) || target.instance != "" && !names.Instance(target.instance) {
		s.apiFail(w, http.StatusNotFound, "not_found", "There is no such workspace.")
		return
	}
	if err := s.hub.CanReadFiles(r.Context(), user, target.project); err != nil {
		s.problem(w, r, err)
		return
	}
	if r.URL.Query().Has("view") {
		s.viewFile(w, r, filesCall{target: target, user: user, path: "/" + r.PathValue("path")})
		return
	}
	// the hub's own policy is for its pages; what comes back here gets one that runs nothing (filesAnswered)
	w.Header().Del("Content-Security-Policy")
	asked := "/" + r.PathValue("path")
	ctx := context.WithValue(r.Context(), filesRequest{}, filesCall{target: target, user: user, path: asked})
	s.filesProxy().ServeHTTP(w, r.WithContext(ctx))
}

type filesCall struct {
	target filesTarget
	user   store.User
	path   string
}

func (s *Server) filesProxy() *httputil.ReverseProxy {
	s.filesState.once.Do(func() {
		transport := &http.Transport{
			DialContext:            s.dialFiles,
			DisableCompression:     true,
			MaxConnsPerHost:        filesConnsPerVolume,
			MaxIdleConnsPerHost:    filesConnsPerVolume,
			IdleConnTimeout:        time.Minute,
			ResponseHeaderTimeout:  30 * time.Second,
			MaxResponseHeaderBytes: 16 << 10,
		}
		s.filesState.proxy = &httputil.ReverseProxy{
			Rewrite:        s.rewriteFiles,
			Transport:      transport,
			ModifyResponse: s.filesAnswered,
			ErrorHandler:   s.filesFailed,
			ErrorLog:       slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
		}
	})
	return s.filesState.proxy
}

// poolKey names a volume for the transport, which keeps connections by address: one volume's never serve another's.
func (t filesTarget) poolKey() string {
	sum := sha256.Sum256([]byte(t.project + "\x00" + t.workspace + "\x00" + t.instance))
	return hex.EncodeToString(sum[:12]) + ".files"
}

func (s *Server) rewriteFiles(pr *httputil.ProxyRequest) {
	call := pr.In.Context().Value(filesRequest{}).(filesCall)
	key := call.target.poolKey()
	header := http.Header{}
	for _, name := range filesRequestHeaders {
		if value := pr.In.Header.Get(name); value != "" {
			header.Set(name, value)
		}
	}
	pr.Out.Header = header
	pr.Out.Host = "files"
	pr.Out.URL.Scheme, pr.Out.URL.Host = "http", key
	pr.Out.URL.Path, pr.Out.URL.RawPath, pr.Out.URL.RawQuery = call.path, "", ""
}

// dialFiles connects to the file server of the request's volume. The transport dials with the values of the context of the request that
// asked for the connection, which say the volume; the address, its pool key, must be that volume's.
func (s *Server) dialFiles(ctx context.Context, _, address string) (net.Conn, error) {
	key, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	call, ok := ctx.Value(filesRequest{}).(filesCall)
	if !ok || call.target.poolKey() != key {
		return nil, errors.New("no such volume")
	}
	t := call.target
	dialing, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return s.hub.DialFiles(dialing, t.project, t.workspace, t.instance)
}

// filesAnswered keeps what the browser needs of the answer, and nothing else. A file is a download, under a policy that runs nothing;
// a refusal becomes the hub's own error.
func (s *Server) filesAnswered(response *http.Response) error {
	call := response.Request.Context().Value(filesRequest{}).(filesCall)
	kept := http.Header{}
	for _, name := range filesResponseHeaders {
		if value := response.Header.Get(name); value != "" {
			kept.Set(name, value)
		}
	}
	kept.Set("X-Content-Type-Options", "nosniff")
	kept.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	kept.Set("Cache-Control", "no-store")
	switch {
	case response.StatusCode == http.StatusMovedPermanently:
		// a directory asked without its slash: the server answers with its name and a slash, nothing else is followed
		if location := response.Header.Get("Location"); location == path.Base(strings.TrimSuffix(call.path, "/"))+"/" {
			kept.Set("Location", location)
		}
	case response.StatusCode >= 400:
		message := serverSaid(response)
		response.Body.Close()
		body, _ := json.Marshal(apiError{Code: "files", Message: message})
		response.Body = io.NopCloser(bytes.NewReader(body))
		response.ContentLength = int64(len(body))
		kept.Set("Content-Type", "application/json")
		kept.Set("Content-Length", strconv.Itoa(len(body)))
	case strings.HasSuffix(call.path, "/"):
		kept.Set("Content-Type", "application/json")
	default:
		kept.Set("Content-Type", "application/octet-stream")
		kept.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(call.path)}))
		starts := response.StatusCode == http.StatusOK || strings.HasPrefix(response.Header.Get("Content-Range"), "bytes 0-")
		if response.Request.Method == http.MethodGet && starts && (response.StatusCode == http.StatusOK || response.StatusCode == http.StatusPartialContent) {
			s.hub.FileDownloaded(response.Request.Context(), call.user, call.target.project, call.target.workspace, call.target.instance, call.path)
		}
	}
	response.Header = kept
	return nil
}

// serverSaid is the file server's refusal, as one line of the hub's.
func serverSaid(response *http.Response) string {
	text, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	message := termsafe.Truncate(termsafe.Line(strings.TrimSpace(string(text))), 200)
	if message == "" {
		message = http.StatusText(response.StatusCode)
	}
	return capitalize(message) + "."
}

// viewJSON is a file shown in the hub: its start as text, or why it is not text. The text is shown as text, never as a page.
type viewJSON struct {
	Size      int64  `json:"size"`
	Kind      string `json:"kind"`             // text or binary
	Text      string `json:"text"`             // the first textview.Limit bytes, lines cut at textview.MaxLine
	Binary    string `json:"binary,omitempty"` // why it is not text: the format its bytes say, or what they are not
	Truncated bool   `json:"truncated"`        // the file goes on past the text
	CutLines  int    `json:"cutLines"`
	Bidi      int    `json:"bidi"` // characters that change the direction of text, which the viewer shows
}

// viewFile reads the start of a file through the file server, at most textview.Limit bytes, and says it as text if its bytes are text.
func (s *Server) viewFile(w http.ResponseWriter, r *http.Request, call filesCall) {
	if strings.HasSuffix(call.path, "/") {
		s.apiFail(w, http.StatusBadRequest, "not_a_file", "A folder is listed, not viewed.")
		return
	}
	s.filesProxy() // its transport
	ctx := context.WithValue(r.Context(), filesRequest{}, call)
	asked := &url.URL{Scheme: "http", Host: call.target.poolKey(), Path: call.path}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asked.String(), nil)
	if err != nil {
		s.apiFail(w, http.StatusBadRequest, "not_a_file", "Not a path.")
		return
	}
	request.Host = "files"
	request.Header.Set("Range", "bytes=0-"+strconv.Itoa(textview.Limit-1))
	response, err := s.filesState.proxy.Transport.RoundTrip(request)
	if err != nil {
		s.filesFailed(w, r, err)
		return
	}
	defer response.Body.Close()
	var size int64
	switch response.StatusCode {
	case http.StatusPartialContent:
		// bytes 0-N/SIZE
		_, total, _ := strings.Cut(response.Header.Get("Content-Range"), "/")
		if size, err = strconv.ParseInt(total, 10, 64); err != nil {
			s.apiFail(w, http.StatusBadGateway, "files", "The file server answered what the hub does not read.")
			return
		}
	case http.StatusOK:
		size = response.ContentLength
	case http.StatusRequestedRangeNotSatisfiable: // no byte to range over: an empty file
	case http.StatusMovedPermanently:
		s.apiFail(w, http.StatusBadRequest, "not_a_file", "It is a folder.")
		return
	default:
		status := http.StatusBadGateway
		if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusForbidden {
			status = response.StatusCode
		}
		s.apiFail(w, status, "files", serverSaid(response))
		return
	}
	var data []byte
	if response.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		if data, err = io.ReadAll(io.LimitReader(response.Body, textview.Limit)); err != nil {
			s.filesFailed(w, r, err)
			return
		}
	}
	truncated := size > int64(len(data))
	seen := textview.Inspect(data, truncated)
	s.hub.FileViewed(r.Context(), call.user, call.target.project, call.target.workspace, call.target.instance, call.path)
	out := viewJSON{Size: max(size, int64(len(data))), Kind: "text", Text: seen.Text, Truncated: truncated, CutLines: seen.CutLines, Bidi: seen.Bidi}
	if seen.Binary != "" {
		out = viewJSON{Size: out.Size, Kind: "binary", Binary: seen.Binary}
	}
	writeJSON(w, http.StatusOK, out)
}

// filesFailed says why the file server could not be reached. What a link said is not shown as it is.
func (s *Server) filesFailed(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		return // the person left
	case errors.As(err, new(hub.OfflineError)):
		s.problem(w, r, err)
	case strings.Contains(err.Error(), "no volume") || strings.Contains(err.Error(), "not a workspace") || strings.Contains(err.Error(), "no workspace"):
		s.apiFail(w, http.StatusNotFound, "not_found", "There is no such workspace, or its volume is gone.")
	default:
		s.log.Info("the files of a workspace could not be reached", "err", err)
		s.apiFail(w, http.StatusBadGateway, "files_unreachable", "The project's file server could not be reached. Try again.")
	}
}
