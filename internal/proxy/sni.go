// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"
)

const (
	recordTypeHandshake   = 0x16
	handshakeClientHello  = 0x01
	extensionServerName   = 0x0000
	extensionECH          = 0xfe0d // the final one, which echHello in the tests sends
	maxClientHelloRecord  = 16 << 10
	clientHelloReadWindow = 10 * time.Second
)

var (
	errNotClientHello     = errors.New("the first bytes are not a TLS ClientHello")
	errDuplicateExtension = errors.New("the ClientHello repeats an extension")
)

// The extension codepoints of Encrypted Client Hello. With ECH the name in the clear can differ from
// the real one, which defeats the check that the tunnel carries TLS for the host it was approved for.
// The parser reports any of them so the proxy can terminate TLS for such a hello instead of tunneling
// it. Every codepoint the drafts used counts, and the legacy ESNI extension too: terminating is the safe
// branch, and a client that sends one that is no longer standard is rare.
var echExtensions = map[uint16]string{
	0xfe08: "encrypted_client_hello, an ECH draft (08)",
	0xfe09: "encrypted_client_hello, an ECH draft (09)",
	0xfe0a: "encrypted_client_hello, an ECH draft (10)",
	0xfe0b: "encrypted_client_hello, an ECH draft (11)",
	0xfe0c: "encrypted_client_hello, an ECH draft (12)",
	0xfe0d: "encrypted_client_hello, the final codepoint, from ECH draft 13 on",
	0xff03: "encrypted_client_hello, an early ECH draft",
	0xffce: "encrypted_server_name, the legacy ESNI that ECH replaced",
}

// readClientHello reads the client's first TLS record and returns the raw bytes, which must be
// replayed to whoever serves the connection, the server name the client asks for ("" when it sends none)
// and whether it carries an Encrypted Client Hello extension.
func readClientHello(conn net.Conn) (raw []byte, serverName string, ech bool, err error) {
	conn.SetReadDeadline(time.Now().Add(clientHelloReadWindow))
	defer conn.SetReadDeadline(time.Time{})

	header := make([]byte, 5)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, "", false, err
	}
	if header[0] != recordTypeHandshake {
		return nil, "", false, errNotClientHello
	}
	length := int(binary.BigEndian.Uint16(header[3:5]))
	if length == 0 || length > maxClientHelloRecord {
		return nil, "", false, errNotClientHello
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return nil, "", false, err
	}
	raw = append(header, payload...)
	name, ech, err := parseServerName(payload)
	return raw, name, ech, err
}

// parseServerName extracts the SNI from the handshake message of a ClientHello record, and whether the
// hello carries an Encrypted Client Hello extension.
func parseServerName(hello []byte) (name string, ech bool, err error) {
	r := reader(hello)
	if kind, ok := r.byte(); !ok || kind != handshakeClientHello {
		return "", false, errNotClientHello
	}
	if _, ok := r.skip(3 + 2 + 32); !ok { // handshake length, version, random
		return "", false, errNotClientHello
	}
	for _, width := range []int{1, 2, 1} { // session id, cipher suites, compression methods
		length, ok := r.length(width)
		if !ok {
			return "", false, errNotClientHello
		}
		if _, ok := r.skip(length); !ok {
			return "", false, errNotClientHello
		}
	}
	extensionsLength, ok := r.length(2)
	if !ok {
		return "", false, nil // no extensions at all: no server name
	}
	extensions, ok := r.take(extensionsLength)
	if !ok {
		return "", false, errNotClientHello
	}
	seen := map[uint16]bool{}
	for len(extensions) >= 4 {
		kind := binary.BigEndian.Uint16(extensions[:2])
		size := int(binary.BigEndian.Uint16(extensions[2:4]))
		if len(extensions) < 4+size {
			return "", false, errNotClientHello
		}
		body := extensions[4 : 4+size]
		extensions = extensions[4+size:]
		if seen[kind] { // illegal, and parsers disagree on which one counts
			return "", false, errDuplicateExtension
		}
		seen[kind] = true
		if _, isECH := echExtensions[kind]; isECH {
			ech = true
		}
		switch kind {
		case extensionServerName:
			found, err := serverNameFrom(body)
			if err != nil {
				return "", false, err
			}
			if name == "" {
				name = found
			}
		}
	}
	return name, ech, nil
}

// serverNameFrom reads the host_name entry of a server_name extension.
func serverNameFrom(body []byte) (string, error) {
	list := reader(body)
	if _, ok := list.length(2); !ok {
		return "", errNotClientHello
	}
	for {
		nameType, ok := list.byte()
		if !ok {
			return "", nil
		}
		nameLength, ok := list.length(2)
		if !ok {
			return "", errNotClientHello
		}
		entry, ok := list.take(nameLength)
		if !ok {
			return "", errNotClientHello
		}
		if nameType == 0 {
			return string(entry), nil
		}
	}
}

type reader []byte

func (r *reader) byte() (byte, bool) {
	if len(*r) < 1 {
		return 0, false
	}
	b := (*r)[0]
	*r = (*r)[1:]
	return b, true
}

func (r *reader) take(n int) ([]byte, bool) {
	if n < 0 || len(*r) < n {
		return nil, false
	}
	out := (*r)[:n]
	*r = (*r)[n:]
	return out, true
}

func (r *reader) skip(n int) (struct{}, bool) {
	_, ok := r.take(n)
	return struct{}{}, ok
}

func (r *reader) length(width int) (int, bool) {
	raw, ok := r.take(width)
	if !ok {
		return 0, false
	}
	n := 0
	for _, b := range raw {
		n = n<<8 | int(b)
	}
	return n, true
}
