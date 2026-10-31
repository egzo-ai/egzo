// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

// fakehub is a small hub for the specs: core's real hub side of the link protocol, over TLS with a certificate of its own, and a
// plain control API on loopback where a spec plays the person who approves and the hub that asks.
//
//	fakehub -link 0.0.0.0:0 -control 127.0.0.1:0 -host 172.17.0.1 -ca /path/to/ca.pem
//
// It prints the two addresses on its first line of output, as `link=ADDR control=ADDR`.
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/egzo-ai/egzo/internal/hublink"
)

type registration struct {
	Project     string
	Key         string
	Host        string
	Templates   []string
	State       string
	Code        string
	Owner       string
	ID          string
	Connections int
	Online      bool

	peer  *hublink.Peer
	feeds map[string][]json.RawMessage
}

type hub struct {
	origin string

	mu    sync.Mutex
	regs  map[string]*registration // by key
	gone  []string                 // the projects that asked to be forgotten
	count int
}

func (h *hub) verdict(r *registration) hublink.Verdict {
	v := hublink.Verdict{State: r.State, Project: r.ID, Owner: r.Owner}
	if r.State == hublink.StatePending {
		v.Code, v.Address = r.Code, h.origin+"/link/"+r.Code
	}
	return v
}

func (h *hub) link(w http.ResponseWriter, r *http.Request) {
	session, err := hublink.Accept(w, r)
	if err != nil {
		return
	}
	defer session.Close()
	peer, err := hublink.Handshake(r.Context(), session, h.origin)
	if err != nil {
		log.Printf("handshake: %v", err)
		return
	}
	key := hex.EncodeToString(peer.Hello.Key)
	h.mu.Lock()
	reg := h.regs[key]
	if reg == nil {
		h.count++
		reg = &registration{
			Project: peer.Hello.Project, Key: key, State: hublink.StatePending, Code: fmt.Sprintf("CODE-%04d", h.count),
			feeds: map[string][]json.RawMessage{},
		}
		h.regs[key] = reg
	}
	reg.Host, reg.Templates, reg.peer, reg.Online = peer.Hello.Host, peer.Hello.Templates, peer, true
	reg.Connections++
	verdict := h.verdict(reg)
	h.mu.Unlock()
	if err := peer.Send(verdict); err != nil {
		return
	}
	defer func() {
		h.mu.Lock()
		if reg.peer == peer {
			reg.peer, reg.Online = nil, false
		}
		h.mu.Unlock()
	}()
	record := func(feed string, item any) {
		data, _ := json.Marshal(item)
		h.mu.Lock()
		defer h.mu.Unlock()
		reg.feeds[feed] = append(reg.feeds[feed], data)
	}
	_ = peer.Run(r.Context(), hublink.Handlers{
		Project: func(u hublink.ProjectUpdate) { record(hublink.FeedProject, u) },
		Status:  func(s hublink.StatusLine) { record(hublink.FeedStatus, s) },
		Audit:   func(e hublink.AuditEvent) { record(hublink.FeedAudit, e) },
		Unlink: func() hublink.Verdict {
			h.mu.Lock()
			defer h.mu.Unlock()
			delete(h.regs, key)
			h.gone = append(h.gone, reg.Project)
			return hublink.Verdict{State: hublink.StateUnlinked}
		},
	})
}

// find is the newest registration of a project's name.
func (h *hub) find(project string) *registration {
	h.mu.Lock()
	defer h.mu.Unlock()
	var found *registration
	for _, r := range h.regs {
		if r.Project == project && (found == nil || r.Connections > 0 && found.peer == nil) {
			found = r
		}
	}
	return found
}

func answer(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

type verbRequest struct {
	Project, Verb, Template, Name, Actor, Agent, Container, Input string
	Tail, Limit                                                   int
	Seconds                                                       float64
}

func (h *hub) verb(ctx context.Context, peer *hublink.Peer, q verbRequest) (any, []string, error) {
	var steps []string
	progress := func(step string) { steps = append(steps, step) }
	switch q.Verb {
	case hublink.VerbProject:
		result, err := peer.Project(ctx)
		return result, steps, err
	case hublink.VerbPublished:
		result, err := peer.Published(ctx)
		return result, steps, err
	case hublink.VerbSpawn:
		result, err := peer.Spawn(ctx, q.Template, q.Name, q.Actor, progress)
		return result, steps, err
	case hublink.VerbStart:
		return nil, steps, peer.Start(ctx, q.Agent)
	case hublink.VerbStop:
		return nil, steps, peer.Stop(ctx, q.Agent)
	case hublink.VerbRemove:
		return nil, steps, peer.Remove(ctx, q.Agent, progress)
	case hublink.VerbEvents:
		result, err := peer.Events(ctx, q.Agent, q.Limit)
		return result, steps, err
	case hublink.VerbLogs:
		result, err := peer.Logs(ctx, q.Container, q.Tail)
		return result, steps, err
	case hublink.VerbSpec:
		result, err := peer.Spec(ctx)
		return string(result), steps, err
	case hublink.VerbAttach:
		var out bytes.Buffer
		input, writer := io.Pipe()
		attached, cancel := context.WithTimeout(ctx, time.Duration(q.Seconds*float64(time.Second)))
		defer cancel()
		go func() { _, _ = writer.Write([]byte(q.Input)); <-attached.Done(); writer.Close() }()
		err := peer.Attach(attached, q.Agent, hublink.Terminal{In: input, Out: &out, Size: hublink.Size{Cols: 80, Rows: 24}})
		return out.String(), steps, err
	}
	return nil, steps, peer.Ask(ctx, q.Verb) // a verb the protocol does not have
}

func (h *hub) control() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /registrations", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		list := []*registration{}
		for _, reg := range h.regs {
			list = append(list, reg)
		}
		answer(w, map[string]any{"registrations": list, "forgotten": h.gone})
	})
	mux.HandleFunc("POST /decide", func(w http.ResponseWriter, r *http.Request) {
		var q struct{ Project, State, Owner string }
		_ = json.NewDecoder(r.Body).Decode(&q)
		reg := h.find(q.Project)
		if reg == nil {
			http.Error(w, "no such registration", http.StatusNotFound)
			return
		}
		h.mu.Lock()
		reg.State, reg.Owner = q.State, q.Owner
		if q.State == hublink.StateApproved {
			reg.ID = "p-" + reg.Key[:8]
		}
		peer, verdict := reg.peer, h.verdict(reg)
		h.mu.Unlock()
		if peer != nil {
			_ = peer.Send(verdict)
		}
		answer(w, reg)
	})
	mux.HandleFunc("POST /verb", func(w http.ResponseWriter, r *http.Request) {
		var q verbRequest
		_ = json.NewDecoder(r.Body).Decode(&q)
		reg := h.find(q.Project)
		h.mu.Lock()
		var peer *hublink.Peer
		if reg != nil {
			peer = reg.peer
		}
		h.mu.Unlock()
		if peer == nil {
			answer(w, map[string]any{"error": "not connected"})
			return
		}
		limit := 20 * time.Second
		if q.Verb == hublink.VerbSpawn || q.Verb == hublink.VerbRemove {
			limit = 2 * time.Minute
		}
		if q.Limit > 0 && q.Verb == hublink.VerbProject {
			limit = time.Duration(q.Limit) * time.Second // a spec that expects no answer
		}
		ctx, cancel := context.WithTimeout(r.Context(), limit)
		defer cancel()
		result, steps, err := h.verb(ctx, peer, q)
		out := map[string]any{"result": result, "steps": steps}
		if err != nil {
			out["error"] = err.Error()
		}
		answer(w, out)
	})
	mux.HandleFunc("GET /feed", func(w http.ResponseWriter, r *http.Request) {
		reg := h.find(r.URL.Query().Get("project"))
		items := []json.RawMessage{}
		if reg != nil {
			h.mu.Lock()
			items = append(items, reg.feeds[r.URL.Query().Get("feed")]...)
			h.mu.Unlock()
		}
		answer(w, items)
	})
	mux.HandleFunc("POST /drop", func(w http.ResponseWriter, r *http.Request) {
		reg := h.find(r.URL.Query().Get("project"))
		h.mu.Lock()
		defer h.mu.Unlock()
		if reg != nil && reg.peer != nil {
			reg.peer.Close()
		}
	})
	return mux
}

func certificate(host, caPath string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "egzo fake hub"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP(host)},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

func main() {
	linkAddress := flag.String("link", "0.0.0.0:0", "")
	controlAddress := flag.String("control", "127.0.0.1:0", "")
	host := flag.String("host", "", "the address links reach this hub at")
	caPath := flag.String("ca", "", "where to write the CA")
	flag.Parse()

	cert, err := certificate(*host, *caPath)
	if err != nil {
		log.Fatal(err)
	}
	links, err := tls.Listen("tcp", *linkAddress, &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		log.Fatal(err)
	}
	control, err := net.Listen("tcp", *controlAddress)
	if err != nil {
		log.Fatal(err)
	}
	port := links.Addr().(*net.TCPAddr).Port
	h := &hub{origin: fmt.Sprintf("https://%s:%d", *host, port), regs: map[string]*registration{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+hublink.Path, h.link)
	go func() { log.Fatal(http.Serve(links, mux)) }()
	fmt.Printf("link=%s control=%s\n", strings.TrimPrefix(h.origin, "https://"), control.Addr())
	log.Fatal(http.Serve(control, h.control()))
}
