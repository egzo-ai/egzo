// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// maxScannedBody bounds a request body the proxy buffers to swap placeholders in it.
const maxScannedBody = 1 << 20

// swapper replaces placeholders with secret values in the requests to one host. The value is encoded
// for the place it lands in, so a secret with a quote, an ampersand or a space cannot break out of a
// form field, a JSON string or a URL.
type swapper struct {
	raw, query, path, form, json *strings.Replacer
	secrets                      []string
	placeholders                 []string
}

func newSwapper(subs []Substitution) *swapper {
	if len(subs) == 0 {
		return nil
	}
	s := &swapper{}
	var raw, query, path, form, jsonPairs []string
	for _, sub := range subs {
		s.placeholders = append(s.placeholders, sub.Placeholder)
		s.secrets = append(s.secrets, sub.Secret)
		raw = append(raw, sub.Placeholder, sub.Secret)
		query = append(query, sub.Placeholder, url.QueryEscape(sub.Secret))
		path = append(path, sub.Placeholder, url.PathEscape(sub.Secret))
		form = append(form, sub.Placeholder, url.QueryEscape(sub.Secret))
		jsonPairs = append(jsonPairs, sub.Placeholder, jsonEscape(sub.Secret))
	}
	// One replacer per place: a single pass, so a secret that contains another placeholder is not swapped again.
	s.raw, s.query, s.path, s.form, s.json = strings.NewReplacer(raw...), strings.NewReplacer(query...),
		strings.NewReplacer(path...), strings.NewReplacer(form...), strings.NewReplacer(jsonPairs...)
	return s
}

// jsonEscape is the content of a JSON string for text, without the quotes and without escaping <, > and &.
func jsonEscape(text string) string {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.Encode(text)
	encoded := strings.TrimSuffix(out.String(), "\n")
	return encoded[1 : len(encoded)-1]
}

// isUpgrade tells a protocol upgrade (WebSocket), which carries no body to scan, the way
// net/http/httputil.ReverseProxy does: the Connection header has the "upgrade" token.
func isUpgrade(r *http.Request) bool {
	if r.Header.Get("Upgrade") == "" {
		return false
	}
	for _, value := range r.Header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}

func (s *swapper) mentions(text string) bool {
	for _, placeholder := range s.placeholders {
		if strings.Contains(text, placeholder) {
			return true
		}
	}
	return false
}

// apply swaps the placeholders in r, in place. When the request must be refused it returns the status
// and a reason that is safe to log and to show: never a value, a body or a query string.
func (s *swapper) apply(r *http.Request) (status int, reason string) {
	// Every refusal is decided on the original request, before anything is swapped, and its reason is a fixed
	// sentence: nothing in it comes from the request, so a refusal can never carry a secret back to the agent.
	var data []byte
	scanBody := r.Body != nil && r.Body != http.NoBody && !isUpgrade(r)
	if scanBody {
		// The rule depends on the request alone, never on whether it holds a placeholder: a body the proxy
		// cannot read must not slip a placeholder through, and must not depend on its content.
		for _, encoding := range r.Header.Values("Content-Encoding") {
			for _, token := range strings.Split(encoding, ",") {
				if token = strings.TrimSpace(token); token != "" && !strings.EqualFold(token, "identity") {
					return http.StatusUnsupportedMediaType, "request body is compressed (Content-Encoding) and cannot be scanned for placeholders"
				}
			}
		}
		const tooLarge = "request body over 1 MiB cannot be scanned for placeholders"
		if r.ContentLength > maxScannedBody {
			return http.StatusRequestEntityTooLarge, tooLarge
		}
		var err error
		data, err = io.ReadAll(io.LimitReader(r.Body, maxScannedBody+1))
		r.Body.Close()
		if len(data) > maxScannedBody {
			return http.StatusRequestEntityTooLarge, tooLarge
		}
		if err != nil {
			return http.StatusBadRequest, "could not read the request body"
		}
	}
	// A value that cannot travel in a header is refused before anything is swapped.
	for _, values := range r.Header {
		for _, value := range values {
			if s.mentions(value) && strings.ContainsAny(s.raw.Replace(value), "\r\n") {
				return http.StatusInternalServerError, "a secret value contains a line break and cannot be sent in a header"
			}
		}
	}

	for _, values := range r.Header {
		for i, value := range values {
			if s.mentions(value) {
				values[i] = s.raw.Replace(value)
			}
		}
	}
	if escaped := r.URL.EscapedPath(); s.mentions(escaped) {
		swapped := s.path.Replace(escaped)
		if path, err := url.PathUnescape(swapped); err == nil {
			r.URL.Path, r.URL.RawPath = path, swapped
		}
	}
	if s.mentions(r.URL.RawQuery) {
		r.URL.RawQuery = s.query.Replace(r.URL.RawQuery)
	}
	if !scanBody {
		return 0, ""
	}

	if s.mentions(string(data)) {
		replacer := s.raw
		mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		switch {
		case mediaType == "application/x-www-form-urlencoded":
			replacer = s.form
		case mediaType == "application/json" || strings.HasSuffix(mediaType, "+json"):
			replacer = s.json
		}
		data = []byte(replacer.Replace(string(data)))
	}
	// The body was fully read: send it with its own length, never chunked.
	r.TransferEncoding = nil
	r.ContentLength = int64(len(data))
	if len(data) == 0 {
		r.Body = http.NoBody
		r.Header.Del("Content-Length")
	} else {
		r.Body = io.NopCloser(bytes.NewReader(data))
		r.Header.Set("Content-Length", strconv.Itoa(len(data)))
	}
	return 0, ""
}
