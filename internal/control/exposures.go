// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// An exposure is an agent saying that it serves something on a port of its own loopback, and that a person may open it. Nothing is checked:
// a server may start after it, and stop and start again.
//
// Exposures are kept per agent name, next to the registration, because that is what control knows of an instance. Every removal unregisters
// the agent, which deletes them, so a new instance of a name never inherits an old one's address.
const (
	// maxExposures is how many ports one agent may expose: they go out in every status line.
	maxExposures    = 32
	maxExposureName = 64
	// ProtocolHTTP and ProtocolTCP are what can be exposed. `http` is what a hub serves as a preview; `tcp` is any other stream.
	ProtocolHTTP = "http"
	ProtocolTCP  = "tcp"
	// previewTemplateFile keeps the address template the hub gave the project's link.
	previewTemplateFile = "preview-template"
	maxPreviewTemplate  = 256
	// previewPlaceholder is where an exposure's id goes in a template.
	previewPlaceholder = "%s"
)

// Exposure is one exposed port. ID is made at the first `expose` and kept. Address and Command are not stored: they are what to hand to a
// person, made when asked, and one of them is set.
type Exposure struct {
	Port     int    `json:"port"`
	Name     string `json:"name,omitempty"`
	Protocol string `json:"protocol"`
	ID       string `json:"id"`
	Address  string `json:"address,omitempty"`
	Command  string `json:"command,omitempty"`
}

var exposureIDs = base32.StdEncoding.WithPadding(base32.NoPadding)

// newExposureID is 80 random bits, as 16 characters a host name can hold.
func newExposureID() (string, error) {
	random := make([]byte, 10)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return strings.ToLower(exposureIDs.EncodeToString(random)), nil
}

func (s *server) exposuresFile(agent string) string {
	return filepath.Join(s.dir, "exposures", agent+".json")
}

// readExposures is what an agent has exposed, by port. It runs under exposureMu, or for a list that may be a moment old. A list that cannot
// be read is an error and never an empty list: writing one back would lose the ports in it.
func (s *server) readExposures(agent string) ([]Exposure, error) {
	data, err := os.ReadFile(s.exposuresFile(agent))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Exposure
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("the exposures of %s are damaged: %w", agent, err)
	}
	return list, nil
}

// writeExposures replaces an agent's list in one step: the ids in it are addresses somebody may have been given.
func (s *server) writeExposures(agent string, list []Exposure) error {
	path := s.exposuresFile(agent)
	if len(list) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Port < list[j].Port })
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

// writeAtomic replaces a file in one step, and what it holds is on the disk before it does.
func writeAtomic(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, err = temp.Write(data)
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(temp.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(temp.Name(), path)
	}
	return err
}

func validExposureName(name string) bool {
	return len(name) <= maxExposureName && !strings.ContainsFunc(name, unicode.IsControl)
}

// expose records a port and returns what to hand to a person. Exposing a port again changes its name and protocol, and keeps its id.
func (s *server) expose(agent string, port int, name, protocol string) (Exposure, error) {
	if protocol == "" {
		protocol = ProtocolHTTP
	}
	switch {
	case port < 1 || port > 65535:
		return Exposure{}, badRequest("port must be a number from 1 to 65535")
	case protocol != ProtocolHTTP && protocol != ProtocolTCP:
		return Exposure{}, badRequest("protocol is %s or %s", ProtocolHTTP, ProtocolTCP)
	case !validExposureName(name):
		return Exposure{}, badRequest("name must be one short line, of at most %d bytes", maxExposureName)
	}
	s.exposureMu.Lock()
	defer s.exposureMu.Unlock()
	list, err := s.readExposures(agent)
	if err != nil {
		return Exposure{}, err
	}
	at := -1
	for i, e := range list {
		if e.Port == port {
			at = i
		}
	}
	if at >= 0 && list[at].Name == name && list[at].Protocol == protocol {
		return s.addressed(agent, list[at], s.previewTemplate()), nil
	}
	if at < 0 && len(list) >= maxExposures {
		return Exposure{}, conflict("an agent may expose %d ports: unexpose one first", maxExposures)
	}
	if !s.takeBudget(agent, "expose", time.Now()) {
		return Exposure{}, tooMany("exposed ports")
	}
	if at < 0 {
		id, err := newExposureID()
		if err != nil {
			return Exposure{}, err
		}
		list = append(list, Exposure{Port: port, ID: id})
		at = len(list) - 1
	}
	list[at].Name, list[at].Protocol = name, protocol
	exposure := list[at]
	if err := s.writeExposures(agent, list); err != nil {
		return Exposure{}, err
	}
	s.tellExposure(agent, "exposed", strings.TrimSpace(fmt.Sprintf("%d/%s %s", port, protocol, name)))
	return s.addressed(agent, exposure, s.previewTemplate()), nil
}

// unexpose ends an exposure. One that is not there is what was asked for.
func (s *server) unexpose(agent string, port int) error {
	s.exposureMu.Lock()
	defer s.exposureMu.Unlock()
	list, err := s.readExposures(agent)
	if err != nil {
		return err
	}
	kept := make([]Exposure, 0, len(list))
	for _, e := range list {
		if e.Port != port {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(list) {
		return nil
	}
	if !s.takeBudget(agent, "expose", time.Now()) {
		return tooMany("exposed ports")
	}
	if err := s.writeExposures(agent, kept); err != nil {
		return err
	}
	s.tellExposure(agent, "unexposed", strconv.Itoa(port))
	return nil
}

// tellExposure records a change that is already made. An event that cannot be written does not undo it, so it is no failure of the call:
// a call that failed would be tried again, find nothing to do, and still record nothing. The status log hears of it all the same.
func (s *server) tellExposure(agent, kind, text string) {
	if _, err := s.events.append(Event{Type: kind, Agent: agent, Actor: "agent:" + agent, Text: text}); err != nil {
		logger.Error("cannot record a change of exposures", "agent", agent, "event", kind, "err", err)
		s.registryChanged()
	}
}

// forgetExposures is the end of an agent's: the next instance of the name starts with none.
func (s *server) forgetExposures(agent string) error {
	s.exposureMu.Lock()
	defer s.exposureMu.Unlock()
	return s.writeExposures(agent, nil)
}

// addressed fills in what to hand to a person: the address of a preview the hub serves, or the command that reaches the port from the
// machine that holds the engine.
func (s *server) addressed(agent string, e Exposure, template string) Exposure {
	if template != "" && e.Protocol == ProtocolHTTP {
		e.Address = strings.Replace(template, previewPlaceholder, e.ID, 1)
		return e
	}
	e.Command = fmt.Sprintf("egzo agent port-forward %s %d", agent, e.Port)
	return e
}

// exposedBy is what an agent has exposed, as readers get it.
func (s *server) exposedBy(agent, template string) []Exposure {
	list, err := s.readExposures(agent)
	if err != nil {
		logger.Error("cannot read the exposures of an agent", "agent", agent, "err", err)
		return nil
	}
	for i, e := range list {
		list[i] = s.addressed(agent, e, template)
	}
	return list
}

// previewTemplate is the address template, "" when no hub serves the project.
func (s *server) previewTemplate() string {
	data, err := os.ReadFile(filepath.Join(s.dir, previewTemplateFile))
	if err != nil {
		return ""
	}
	return string(data)
}

// validPreviewTemplate takes a base address with one place for an id, as a hub hands it out.
func validPreviewTemplate(template string) bool {
	if len(template) > maxPreviewTemplate || strings.Count(template, "%") != 1 || strings.Count(template, previewPlaceholder) != 1 {
		return false
	}
	u, err := url.Parse(strings.Replace(template, previewPlaceholder, "id", 1))
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil
}

// putPreviewTemplate is the link handing on what the hub said: an empty body clears it. It is kept on the volume, so a sidecar that is made
// again knows it before the link says so again.
func (s *server) putPreviewTemplate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPreviewTemplate+1))
	if err != nil {
		http.Error(w, "the address template is too long", http.StatusRequestEntityTooLarge)
		return
	}
	template := strings.TrimSpace(string(body))
	if template != "" && !validPreviewTemplate(template) {
		http.Error(w, "the address template is not an address with one %s", http.StatusBadRequest)
		return
	}
	path := filepath.Join(s.dir, previewTemplateFile)
	s.exposureMu.Lock()
	defer s.exposureMu.Unlock()
	if template == "" {
		err = os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	} else {
		err = writeAtomic(path, []byte(template))
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.registryChanged() // the addresses in the status lines change with it
	w.WriteHeader(http.StatusNoContent)
}
