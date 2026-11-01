// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/egzo-ai/egzo/internal/hublink"
)

// An exposure is an agent opening one of the ports its template declares, for the hub or for the local machine. The template says which ports
// an agent may serve and nothing else can open: the stack tells control the list when it spawns the instance (see putPorts).
//
// It lasts as long as the sandbox: a restart ends every exposure, because the server is gone and the agent starts with no memory of it. What
// is exposed is then what this boot of the agent said, and an old address cannot reach something new on its port by accident.
//
// Exposures are kept per agent name, next to the registration, because that is what control knows of an instance. Every removal unregisters
// the agent, which deletes them and the declared ports. An id is made from the registration and the port, so nothing has to be kept for a
// preview to get its id back when it is exposed again, and a new instance of a name, which has a registration of its own, never inherits an old
// one's address.
const (
	// maxExposures is how many ports one agent may declare: they go out in every status line.
	maxExposures = hublink.MaxExposures
	// WhereHub and WhereLocal are the two places a port is opened for.
	WhereHub   = hublink.WhereHub
	WhereLocal = hublink.WhereLocal
	// previewTemplateFile keeps the address template the hub gave the project's link.
	previewTemplateFile = "preview-template"
	// UnexposeNote is what an agent is told when it closes a port.
	UnexposeNote = "Active connections may remain active - close your server to end them."
)

// ProtocolHTTP, ProtocolTCP and ProtocolUDP are what a port speaks.
const (
	ProtocolHTTP = hublink.ProtocolHTTP
	ProtocolTCP  = hublink.ProtocolTCP
	ProtocolUDP  = hublink.ProtocolUDP
)

var portNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Port is one declared port as the stack tells control: what the template says, and the address of the relay that reaches it from the machine
// when the instance has one.
type Port struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	// Local is the address people use when the port is opened with where=local; empty when the instance has no relay.
	Local string `json:"local,omitempty"`
}

// Exposure is one open port, as an agent and the status feed see it. ID is set for a preview, and is the same every time this agent exposes
// this port. Address is not stored: it is made when asked.
type Exposure struct {
	Name     string `json:"name"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Where    string `json:"where"`
	ID       string `json:"id,omitempty"`
	Address  string `json:"address"`
}

// opened is what is stored of an exposure: the rest is made from the declared ports.
type opened struct {
	Name  string `json:"name"`
	Where string `json:"where"`
}

var exposureIDs = base32.StdEncoding.WithPadding(base32.NoPadding)

// exposureID is 80 bits nobody can work out without the project key, as 16 characters a host name can hold.
func (s *server) exposureID(agent string, port int) (string, error) {
	key, err := s.projectKey()
	if err != nil {
		return "", err
	}
	path, ok := s.agentFile(agent)
	if !ok {
		return "", errors.New("invalid agent name")
	}
	nonce, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "exposure:%s:%s:%d", agent, strings.TrimSpace(string(nonce)), port)
	return strings.ToLower(exposureIDs.EncodeToString(mac.Sum(nil)[:10])), nil
}

func (s *server) exposuresFile(agent string) string {
	return filepath.Join(s.dir, "exposures", agent+".json")
}

func (s *server) portsFile(agent string) string {
	return filepath.Join(s.dir, "ports", agent+".json")
}

// readPorts is what an agent declares. An agent with no file has no ports.
func (s *server) readPorts(agent string) (map[string]Port, error) {
	data, err := os.ReadFile(s.portsFile(agent))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ports map[string]Port
	if err := json.Unmarshal(data, &ports); err != nil {
		return nil, fmt.Errorf("the ports of %s are damaged: %w", agent, err)
	}
	return ports, nil
}

// putPorts is the stack telling control which ports a new instance declares, and the address of each one for the local machine. The list
// comes from the instance's template and is bounded, so a damaged one cannot make the status lines large.
func (s *server) putPorts(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("name")
	if _, ok := s.agentFile(agent); !ok {
		http.Error(w, "invalid agent name", http.StatusBadRequest)
		return
	}
	var ports map[string]Port
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTemplates)).Decode(&ports); err != nil {
		http.Error(w, "expected a JSON object of ports", http.StatusBadRequest)
		return
	}
	if len(ports) > maxExposures {
		http.Error(w, fmt.Sprintf("an agent declares at most %d ports", maxExposures), http.StatusBadRequest)
		return
	}
	for name, port := range ports {
		switch {
		case len(name) > 32 || !portNamePattern.MatchString(name):
			http.Error(w, fmt.Sprintf("%q is not a port name", name), http.StatusBadRequest)
			return
		case port.Port < 1 || port.Port > 65535:
			http.Error(w, "port must be a number from 1 to 65535", http.StatusBadRequest)
			return
		case port.Protocol != ProtocolHTTP && port.Protocol != ProtocolTCP && port.Protocol != ProtocolUDP:
			http.Error(w, "protocol is http, tcp or udp", http.StatusBadRequest)
			return
		case len(port.Local) > 512 || strings.ContainsFunc(port.Local, unicode.IsControl):
			http.Error(w, "the local address is one short line", http.StatusBadRequest)
			return
		}
	}
	if !s.knownAgent(agent) {
		http.Error(w, fmt.Sprintf("no agent %q in this project", agent), http.StatusNotFound)
		return
	}
	data, err := json.Marshal(ports)
	if err == nil {
		s.exposureMu.Lock()
		defer s.exposureMu.Unlock()
		if err = os.MkdirAll(filepath.Dir(s.portsFile(agent)), 0o755); err == nil {
			err = writeAtomic(s.portsFile(agent), data)
		}
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.registryChanged()
	w.WriteHeader(http.StatusNoContent)
}

// readOpened is what an agent has opened. It runs under exposureMu, or for a list that may be a moment old. A list that cannot be read is an
// error and never an empty list: writing one back would lose the ports in it.
func (s *server) readOpened(agent string) ([]opened, error) {
	data, err := os.ReadFile(s.exposuresFile(agent))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []opened
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("the exposures of %s are damaged: %w", agent, err)
	}
	return list, nil
}

// writeOpened replaces an agent's list in one step.
func (s *server) writeOpened(agent string, list []opened) error {
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
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
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

// expose opens a declared port for where. Exposing it again for the same place changes nothing and answers the same; for the other place it is
// refused until the first is closed, so a port is open to one audience at a time.
func (s *server) expose(agent, name, where string) (Exposure, error) {
	if where != WhereHub && where != WhereLocal {
		return Exposure{}, badRequest("where is %q or %q", WhereHub, WhereLocal)
	}
	s.exposureMu.Lock()
	defer s.exposureMu.Unlock()
	ports, err := s.readPorts(agent)
	if err != nil {
		return Exposure{}, err
	}
	port, ok := ports[name]
	if !ok {
		return Exposure{}, badRequest("%s", undeclared(name, ports))
	}
	list, err := s.readOpened(agent)
	if err != nil {
		return Exposure{}, err
	}
	at := slices.IndexFunc(list, func(o opened) bool { return o.Name == name })
	if at >= 0 && list[at].Where != where {
		return Exposure{}, conflict("%s is open for %s: unexpose it first", name, list[at].Where)
	}
	template := s.previewTemplate()
	if at >= 0 && (where != WhereHub || template != "") {
		return s.addressed(agent, name, port, where, template)
	}
	switch where {
	case WhereHub:
		switch {
		case port.Protocol != ProtocolHTTP:
			return Exposure{}, conflict("%s is %s, and only http ports are served by a hub: use where=local", name, port.Protocol)
		case template == "":
			return Exposure{}, conflict("no hub serves this project: use where=local, if the project has local ports")
		}
	case WhereLocal:
		switch {
		case s.localMax == 0:
			return Exposure{}, conflict("local ports are off in this project")
		case port.Local == "":
			return Exposure{}, conflict("%s has no local address: local ports are off for this agent", name)
		}
		count, err := s.localOpen()
		if err != nil {
			return Exposure{}, err
		}
		if count >= s.localMax {
			return Exposure{}, conflict("%d local ports are open in this project, which is the most: unexpose one first", count)
		}
	}
	if !s.takeBudget(agent, "expose", time.Now()) {
		return Exposure{}, tooMany("exposed ports")
	}
	exposure, err := s.addressed(agent, name, port, where, template)
	if err != nil {
		return Exposure{}, err
	}
	if err := s.writeOpened(agent, append(list, opened{Name: name, Where: where})); err != nil {
		return Exposure{}, err
	}
	s.tellExposure(agent, "exposed", name+" "+where)
	return exposure, nil
}

// undeclared is the refusal of a name the template does not declare, with the names it does.
func undeclared(name string, ports map[string]Port) string {
	if len(ports) == 0 {
		return fmt.Sprintf("this agent declares no ports, so there is no %q to expose", name)
	}
	return fmt.Sprintf("this agent has no port named %q: its ports are %s", name, strings.Join(slices.Sorted(maps.Keys(ports)), ", "))
}

// localOpen counts the ports open for the local machine in the whole project. It runs under exposureMu.
func (s *server) localOpen() (int, error) {
	count := 0
	for _, agent := range s.projectAgents() {
		list, err := s.readOpened(agent)
		if err != nil {
			return 0, err
		}
		for _, o := range list {
			if o.Where == WhereLocal {
				count++
			}
		}
	}
	return count, nil
}

// unexpose closes a port. One that is not open is what was asked for. Connections that are open stay until they end.
func (s *server) unexpose(agent, name string) error {
	s.exposureMu.Lock()
	defer s.exposureMu.Unlock()
	list, err := s.readOpened(agent)
	if err != nil {
		return err
	}
	kept := slices.DeleteFunc(slices.Clone(list), func(o opened) bool { return o.Name == name })
	if len(kept) == len(list) {
		return nil
	}
	if !s.takeBudget(agent, "expose", time.Now()) {
		return tooMany("exposed ports")
	}
	if err := s.writeOpened(agent, kept); err != nil {
		return err
	}
	s.tellExposure(agent, "unexposed", name)
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

// boot ends what an earlier boot of the sandbox exposed. A list that cannot be read goes too: nothing in it is meant to last.
func (s *server) boot(agent string) error {
	s.exposureMu.Lock()
	defer s.exposureMu.Unlock()
	list, unreadable := s.readOpened(agent)
	if unreadable != nil {
		logger.Error("cannot read the exposures of an agent", "agent", agent, "err", unreadable)
	}
	if err := s.writeOpened(agent, nil); err != nil {
		return err
	}
	if unreadable != nil {
		s.registryChanged()
	}
	for _, o := range list {
		s.tellExposure(agent, "unexposed", o.Name)
	}
	return nil
}

// forgetExposures is the end of an agent's: the next instance of the name starts with none, and declares its own ports.
func (s *server) forgetExposures(agent string) error {
	s.exposureMu.Lock()
	defer s.exposureMu.Unlock()
	if err := os.Remove(s.portsFile(agent)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.writeOpened(agent, nil)
}

// addressed makes the answer for an open port: the address of the preview the hub serves, or the one on the local machine.
func (s *server) addressed(agent, name string, port Port, where, template string) (Exposure, error) {
	e := Exposure{Name: name, Port: port.Port, Protocol: port.Protocol, Where: where}
	if where == WhereLocal {
		e.Address = port.Local
		return e, nil
	}
	id, err := s.exposureID(agent, port.Port)
	if err != nil {
		return Exposure{}, err
	}
	e.ID = id
	e.Address = strings.Replace(template, hublink.PreviewPlaceholder, id, 1)
	return e, nil
}

// exposedBy is what an agent has open, as readers get it.
func (s *server) exposedBy(agent, template string) []Exposure {
	list, err := s.readOpened(agent)
	if err != nil {
		logger.Error("cannot read the exposures of an agent", "agent", agent, "err", err)
		return nil
	}
	ports, err := s.readPorts(agent)
	if err != nil {
		logger.Error("cannot read the ports of an agent", "agent", agent, "err", err)
		return nil
	}
	var out []Exposure
	for _, o := range list {
		port, ok := ports[o.Name]
		if !ok {
			continue
		}
		if e, err := s.addressed(agent, o.Name, port, o.Where, template); err == nil {
			out = append(out, e)
		}
	}
	return out
}

// previewTemplate is the address template, "" when no hub serves the project.
func (s *server) previewTemplate() string {
	data, err := os.ReadFile(filepath.Join(s.dir, previewTemplateFile))
	if err != nil {
		return ""
	}
	return string(data)
}

// putPreviewTemplate is the link handing on what the hub said: an empty body clears it. It is kept on the volume, so a sidecar that is made
// again knows it before the link says so again.
func (s *server) putPreviewTemplate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, hublink.MaxPreviewTemplate+1))
	if err != nil {
		http.Error(w, "the address template is too long", http.StatusRequestEntityTooLarge)
		return
	}
	template := strings.TrimSpace(string(body))
	if template != "" && !hublink.ValidPreviewTemplate(template) {
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
