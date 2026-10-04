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
	maxClientHelloRecord  = 16 << 10
	clientHelloReadWindow = 10 * time.Second
)

var errNotClientHello = errors.New("the first bytes are not a TLS ClientHello")

// readClientHello reads the client's first TLS record and returns the raw bytes, which must be
// replayed to the upstream, and the server name the client asks for ("" when it sends none).
func readClientHello(conn net.Conn) (raw []byte, serverName string, err error) {
	conn.SetReadDeadline(time.Now().Add(clientHelloReadWindow))
	defer conn.SetReadDeadline(time.Time{})

	header := make([]byte, 5)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, "", err
	}
	if header[0] != recordTypeHandshake {
		return nil, "", errNotClientHello
	}
	length := int(binary.BigEndian.Uint16(header[3:5]))
	if length == 0 || length > maxClientHelloRecord {
		return nil, "", errNotClientHello
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return nil, "", err
	}
	raw = append(header, payload...)
	name, err := parseServerName(payload)
	return raw, name, err
}

// parseServerName extracts the SNI from the handshake message of a ClientHello record.
func parseServerName(hello []byte) (string, error) {
	r := reader(hello)
	if kind, ok := r.byte(); !ok || kind != handshakeClientHello {
		return "", errNotClientHello
	}
	if _, ok := r.skip(3 + 2 + 32); !ok { // handshake length, version, random
		return "", errNotClientHello
	}
	for _, width := range []int{1, 2, 1} { // session id, cipher suites, compression methods
		length, ok := r.length(width)
		if !ok {
			return "", errNotClientHello
		}
		if _, ok := r.skip(length); !ok {
			return "", errNotClientHello
		}
	}
	extensionsLength, ok := r.length(2)
	if !ok {
		return "", nil // no extensions at all: no server name
	}
	extensions, ok := r.take(extensionsLength)
	if !ok {
		return "", errNotClientHello
	}
	for len(extensions) >= 4 {
		kind := binary.BigEndian.Uint16(extensions[:2])
		size := int(binary.BigEndian.Uint16(extensions[2:4]))
		if len(extensions) < 4+size {
			return "", errNotClientHello
		}
		body := extensions[4 : 4+size]
		extensions = extensions[4+size:]
		if kind != extensionServerName {
			continue
		}
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
			name, ok := list.take(nameLength)
			if !ok {
				return "", errNotClientHello
			}
			if nameType == 0 {
				return string(name), nil
			}
		}
	}
	return "", nil
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
