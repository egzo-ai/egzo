// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package engine

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// MaxAnswer is the most an engine may send for one answer that is not a stream (a listing, an inspect), and the most a command run
// with Exec may write. The largest real answers are listings of a few thousand containers, a few MiB; an answer without end is
// an engine that wants the reader to keep it all.
const MaxAnswer = 64 << 20

// MaxEventLine is the longest event the engine's event stream may send: one is a few hundred bytes.
const MaxEventLine = 1 << 20

// ErrAnswerTooLarge is what reading an answer of the engine returns past its limit.
type ErrAnswerTooLarge struct {
	What  string // the request, or the command
	Limit int64
}

func (e ErrAnswerTooLarge) Error() string {
	return fmt.Sprintf("the engine sent more than %d MiB for %s: refusing it", e.Limit>>20, e.What)
}

// streamed are the ends of the paths whose answers are streams, which have no size: logs and events, attaching, image pulls and
// copies. What reads them bounds what it keeps (Demux bounds a frame, the event stream a line).
var streamed = []string{"/logs", "/events", "/attach", "/archive", "/images/create", "/build", "/get", "/export", "/stats", "/push", "/load"}

// boundedTransport holds every answer of an engine to MaxAnswer, except the streams, and each event of the event stream to
// MaxEventLine. It is how the hub reads an engine it does not control.
type boundedTransport struct{ next http.RoundTripper }

func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(r)
	if err != nil {
		return resp, err
	}
	what := r.Method + " " + r.URL.Path
	switch {
	case strings.HasSuffix(r.URL.Path, "/events"):
		resp.Body = &lineBounded{ReadCloser: resp.Body, what: what}
	case isStreamed(r.URL.Path):
	default:
		resp.Body = &bounded{ReadCloser: resp.Body, left: MaxAnswer, what: what}
	}
	return resp, nil
}

func isStreamed(path string) bool {
	for _, end := range streamed {
		if strings.HasSuffix(path, end) {
			return true
		}
	}
	return false
}

// bounded is a body that fails past its limit.
type bounded struct {
	io.ReadCloser
	left int64
	what string
}

func (b *bounded) Read(p []byte) (int, error) {
	if b.left <= 0 {
		// one byte more says whether the answer ended right at the limit
		var one [1]byte
		if n, _ := b.ReadCloser.Read(one[:]); n > 0 {
			return 0, ErrAnswerTooLarge{What: b.what, Limit: MaxAnswer}
		}
		return b.ReadCloser.Read(p)
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)
	return n, err
}

// lineBounded is a body of lines that fails on a line longer than MaxEventLine.
type lineBounded struct {
	io.ReadCloser
	line int // bytes since the last newline
	what string
}

func (l *lineBounded) Read(p []byte) (int, error) {
	n, err := l.ReadCloser.Read(p)
	for chunk := p[:n]; len(chunk) > 0; {
		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			l.line += len(chunk)
			break
		}
		l.line, chunk = 0, chunk[i+1:]
	}
	if l.line > MaxEventLine {
		return 0, ErrAnswerTooLarge{What: "one event of " + l.what, Limit: MaxEventLine}
	}
	return n, err
}

// boundedBuffer is where Exec keeps what a command writes, up to MaxAnswer.
type boundedBuffer struct {
	bytes.Buffer
	what string
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > MaxAnswer {
		return 0, ErrAnswerTooLarge{What: b.what, Limit: MaxAnswer}
	}
	return b.Buffer.Write(p)
}
