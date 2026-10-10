// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

const (
	phA = "egzo-ph-0123456789abcdef0123456789abcdef"
	phB = "egzo-ph-fedcba9876543210fedcba9876543210"
)

// awkward values: every one of them breaks a naive swap in some place.
var awkward = []string{
	`pa"ss`, `a&b=c`, `1+1 = 2`, `100%`, `a b`, `x/y/../z`, `héllo ☃ 日本`, `<tag>&amp;`, `back\slash`, `plain`,
}

type seenRequest struct {
	path, rawQuery, rawPath, contentLength string
	contentLen                             int64
	chunked                                bool
	header                                 http.Header
	body                                   string
}

// swapRig has an upstream that records what it receives.
func swapRig(t *testing.T, services ...Service) (*rig, func() seenRequest) {
	t.Helper()
	var last seenRequest
	r := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		data, _ := io.ReadAll(req.Body)
		last = seenRequest{path: req.URL.Path, rawPath: req.URL.EscapedPath(), rawQuery: req.URL.RawQuery,
			contentLength: req.Header.Get("Content-Length"), contentLen: req.ContentLength,
			chunked: len(req.TransferEncoding) > 0, header: req.Header.Clone(), body: string(data)}
		io.WriteString(w, "echo "+phA+" "+req.URL.RawQuery)
	})
	r.setPolicy("coder", "tok", nil, services...)
	return r, func() seenRequest { return last }
}

func ph(secret string) Service {
	return Service{Name: "site", Hosts: []string{"example.com"}, Placeholder: phA, Secret: secret}
}

func do(t *testing.T, r *rig, method, target, contentType string, bodyReader io.Reader, headers ...string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, target, bodyReader)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		request.Header.Set(headers[i], headers[i+1])
	}
	response, err := r.client("coder", "tok", r.ca.Cert).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestPlaceholderInAHeaderIsSwapped(t *testing.T) {
	r, seen := swapRig(t, ph("s3cret"))
	body(t, do(t, r, "GET", "https://example.com/", "", nil, "X-Site-Key", "pre-"+phA+"-post", "X-Other", "keep"))
	if got := seen().header.Get("X-Site-Key"); got != "pre-s3cret-post" {
		t.Errorf("header = %q", got)
	}
	if got := seen().header.Get("X-Other"); got != "keep" {
		t.Errorf("other header = %q", got)
	}
}

func TestHeaderValueIsRaw(t *testing.T) {
	value := `a"b&c d%`
	r, seen := swapRig(t, ph(value))
	body(t, do(t, r, "GET", "https://example.com/", "", nil, "X-Site-Key", phA))
	if got := seen().header.Get("X-Site-Key"); got != value {
		t.Errorf("header = %q, want %q", got, value)
	}
}

func TestSecretWithALineBreakIsNeverSentInAHeader(t *testing.T) {
	r, _ := swapRig(t, ph("a\r\nInjected: yes"))
	response := do(t, r, "GET", "https://example.com/", "", nil, "X-Site-Key", phA)
	text := body(t, response)
	if response.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", response.StatusCode)
	}
	if r.requests() != 0 {
		t.Error("the request was forwarded")
	}
	if strings.Contains(text, "Injected") || strings.Contains(r.audit.String(), "Injected") {
		t.Error("the value leaked")
	}
	if !strings.Contains(r.audit.String(), `"action":"deny"`) {
		t.Errorf("audit:\n%s", r.audit.String())
	}
}

func TestSecretWithALineBreakIsFineInABodyAndAQuery(t *testing.T) {
	r, seen := swapRig(t, ph("a\nb"))
	body(t, do(t, r, "POST", "https://example.com/?k="+phA, "application/json", strings.NewReader(`{"k":"`+phA+`"}`)))
	if seen().body != `{"k":"a\nb"}` || seen().rawQuery != "k=a%0Ab" {
		t.Errorf("body %q query %q", seen().body, seen().rawQuery)
	}
}

func TestPlaceholderInTheQueryIsPercentEncoded(t *testing.T) {
	for _, value := range awkward {
		r, seen := swapRig(t, ph(value))
		body(t, do(t, r, "GET", "https://example.com/p?token="+phA+"&x=1", "", nil))
		parsed, err := url.ParseQuery(seen().rawQuery)
		if err != nil || parsed.Get("token") != value || parsed.Get("x") != "1" {
			t.Errorf("value %q: query %q parsed %v, %v", value, seen().rawQuery, parsed, err)
		}
	}
}

func TestPlaceholderInThePathIsPercentEncoded(t *testing.T) {
	for _, value := range awkward {
		r, seen := swapRig(t, ph(value))
		body(t, do(t, r, "GET", "https://example.com/key/"+phA+"/x", "", nil))
		got := seen()
		if got.path != "/key/"+value+"/x" {
			t.Errorf("value %q: path %q (raw %q)", value, got.path, got.rawPath)
		}
		if strings.Count(got.rawPath, "/") != 3 {
			t.Errorf("value %q: a slash in the value split the path: %q", value, got.rawPath)
		}
	}
}

func TestPlaceholderInAFormBodyIsPercentEncoded(t *testing.T) {
	for _, value := range awkward {
		r, seen := swapRig(t, ph(value))
		body(t, do(t, r, "POST", "https://example.com/login", "application/x-www-form-urlencoded", strings.NewReader("user=me&password="+phA+"&z=1")))
		form, err := url.ParseQuery(seen().body)
		if err != nil || form.Get("password") != value || form.Get("user") != "me" || form.Get("z") != "1" {
			t.Errorf("value %q: body %q parsed %v, %v", value, seen().body, form, err)
		}
	}
}

func TestFormContentTypeWithParameters(t *testing.T) {
	r, seen := swapRig(t, ph("a&b"))
	body(t, do(t, r, "POST", "https://example.com/", "Application/X-WWW-Form-Urlencoded; charset=UTF-8", strings.NewReader("p="+phA)))
	if seen().body != "p=a%26b" {
		t.Errorf("body = %q", seen().body)
	}
}

func TestPlaceholderInAJSONBodyIsEscaped(t *testing.T) {
	for _, contentType := range []string{"application/json", "application/json; charset=utf-8", "application/vnd.api+json"} {
		for _, value := range awkward {
			r, seen := swapRig(t, ph(value))
			body(t, do(t, r, "POST", "https://example.com/login", contentType, strings.NewReader(`{"user":"me","password":"`+phA+`","n":1}`)))
			var parsed map[string]any
			if err := json.Unmarshal([]byte(seen().body), &parsed); err != nil {
				t.Errorf("%s %q: invalid JSON %q: %v", contentType, value, seen().body, err)
				continue
			}
			if parsed["password"] != value || parsed["user"] != "me" || parsed["n"] != float64(1) {
				t.Errorf("%s %q: got %v", contentType, value, parsed)
			}
			if strings.Contains(value, "<") && strings.Contains(seen().body, `\u003c`) {
				t.Errorf("HTML-escaped: %q", seen().body)
			}
		}
	}
}

func TestPlaceholderInOtherBodiesIsRaw(t *testing.T) {
	for _, contentType := range []string{"text/plain", "application/octet-stream", ""} {
		value := `a"b&c d%+/`
		r, seen := swapRig(t, ph(value))
		body(t, do(t, r, "POST", "https://example.com/", contentType, strings.NewReader("pw="+phA+";")))
		if seen().body != "pw="+value+";" {
			t.Errorf("%q: body = %q", contentType, seen().body)
		}
	}
}

func TestEveryOccurrenceAndEveryPlaceholderIsSwapped(t *testing.T) {
	second := Service{Name: "other", Hosts: []string{"example.com"}, Placeholder: phB, Secret: "two&"}
	r, seen := swapRig(t, ph("one"), second)
	body(t, do(t, r, "POST", "https://example.com/"+phB+"?a="+phA+"&b="+phB, "application/x-www-form-urlencoded",
		strings.NewReader(phA+"="+phB+"&"+phA), "X-A", phA, "X-B", phB))
	got := seen()
	if got.path != "/two&" || got.rawQuery != "a=one&b=two%26" || got.body != "one=two%26&one" ||
		got.header.Get("X-A") != "one" || got.header.Get("X-B") != "two&" {
		t.Errorf("got %+v", got)
	}
}

func TestASecretContainingAPlaceholderIsNotSwappedAgain(t *testing.T) {
	second := Service{Name: "other", Hosts: []string{"example.com"}, Placeholder: phB, Secret: "B"}
	r, seen := swapRig(t, ph(phB), second)
	body(t, do(t, r, "POST", "https://example.com/", "text/plain", strings.NewReader(phA)))
	if seen().body != phB {
		t.Errorf("body = %q", seen().body)
	}
}

func TestContentLengthMatchesTheSwappedBody(t *testing.T) {
	r, seen := swapRig(t, ph("a much longer secret value than the placeholder is, ☃"))
	payload := "x=" + phA
	body(t, do(t, r, "POST", "https://example.com/", "text/plain", strings.NewReader(payload)))
	got := seen()
	want := "x=a much longer secret value than the placeholder is, ☃"
	if got.body != want || got.contentLength != strconv.Itoa(len(want)) || got.contentLen != int64(len(want)) || got.chunked {
		t.Errorf("got %+v", got)
	}
	// A shorter secret shrinks it.
	r, seen = swapRig(t, ph("s"))
	body(t, do(t, r, "POST", "https://example.com/", "text/plain", strings.NewReader(payload)))
	if got := seen(); got.body != "x=s" || got.contentLength != "3" {
		t.Errorf("got %+v", got)
	}
}

func TestChunkedRequestBodyIsSwappedAndSentWithItsLength(t *testing.T) {
	r, seen := swapRig(t, ph("secret"))
	// A reader that is not a known type has no length: the client sends it chunked.
	reader := io.MultiReader(strings.NewReader("k="), strings.NewReader(phA))
	body(t, do(t, r, "POST", "https://example.com/", "text/plain", reader))
	got := seen()
	if got.body != "k=secret" || got.contentLength != "8" || got.chunked {
		t.Errorf("got %+v", got)
	}
}

func TestBodylessAndEmptyRequestsPass(t *testing.T) {
	r, seen := swapRig(t, ph("secret"))
	response := do(t, r, "GET", "https://example.com/", "", nil)
	body(t, response)
	if response.StatusCode != 200 {
		t.Errorf("status = %d", response.StatusCode)
	}
	response = do(t, r, "POST", "https://example.com/", "text/plain", strings.NewReader(""))
	body(t, response)
	if response.StatusCode != 200 || seen().body != "" {
		t.Errorf("status = %d, body %q", response.StatusCode, seen().body)
	}
}

func TestBodyOverOneMiBIsRefusedWithAKnownLength(t *testing.T) {
	r, _ := swapRig(t, ph("secret"))
	big := strings.Repeat("a", 1<<20+1)
	response := do(t, r, "POST", "https://example.com/", "text/plain", strings.NewReader(big))
	text := body(t, response)
	if response.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(text, "1 MiB") {
		t.Errorf("status %d, %q", response.StatusCode, text)
	}
	if r.requests() != 0 {
		t.Error("forwarded")
	}
	assertDeny(t, r, "1 MiB")
}

func TestBodyOverOneMiBIsRefusedWithAnUnknownLength(t *testing.T) {
	r, _ := swapRig(t, ph("secret"))
	big := io.MultiReader(strings.NewReader(strings.Repeat("a", 1<<20)), strings.NewReader("b"))
	response := do(t, r, "POST", "https://example.com/", "text/plain", big)
	text := body(t, response)
	if response.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(text, "1 MiB") {
		t.Errorf("status %d, %q", response.StatusCode, text)
	}
	if r.requests() != 0 {
		t.Error("forwarded")
	}
}

func TestBodyOfExactlyOneMiBPasses(t *testing.T) {
	r, seen := swapRig(t, ph("secret"))
	exact := strings.Repeat("a", 1<<20)
	response := do(t, r, "POST", "https://example.com/", "text/plain", io.MultiReader(strings.NewReader(exact)))
	body(t, response)
	if response.StatusCode != 200 || len(seen().body) != 1<<20 {
		t.Errorf("status %d, len %d", response.StatusCode, len(seen().body))
	}
}

func TestCompressedBodyIsRefusedWhetherOrNotItHoldsAPlaceholder(t *testing.T) {
	for _, encoding := range []string{"gzip", "br", "identity, gzip"} {
		r, _ := swapRig(t, ph("secret"))
		response := do(t, r, "POST", "https://example.com/", "text/plain", strings.NewReader("nothing here"), "Content-Encoding", encoding)
		text := body(t, response)
		if response.StatusCode != http.StatusUnsupportedMediaType || !strings.Contains(text, "compressed") {
			t.Errorf("%s: status %d, %q", encoding, response.StatusCode, text)
		}
		if r.requests() != 0 {
			t.Error("forwarded")
		}
		assertDeny(t, r, "compressed")
	}
}

func TestIdentityEncodingIsFine(t *testing.T) {
	r, seen := swapRig(t, ph("secret"))
	body(t, do(t, r, "POST", "https://example.com/", "text/plain", strings.NewReader(phA), "Content-Encoding", "identity"))
	if seen().body != "secret" {
		t.Errorf("body = %q", seen().body)
	}
}

func TestBodyLimitsOnlyApplyToPlaceholderHosts(t *testing.T) {
	r, seen := swapRig(t, Service{Name: "api", Hosts: []string{"example.com"}, Header: "x-api-key", Secret: "k"})
	big := strings.Repeat("a", 2<<20)
	response := do(t, r, "POST", "https://example.com/", "text/plain", strings.NewReader(big), "Content-Encoding", "gzip")
	body(t, response)
	if response.StatusCode != 200 || len(seen().body) != len(big) {
		t.Errorf("status %d: an inject-only host must not be scanned", response.StatusCode)
	}
}

func TestAnotherHostIsUntouched(t *testing.T) {
	r, seen := swapRig(t, ph("secret"))
	r.setPolicy("coder", "tok", []string{"sub.example.com"}, ph("secret"))
	// sub.example.com is tunnelled, never swapped: the client trusts the upstream's own certificate.
	request, _ := http.NewRequest("POST", "https://sub.example.com/?q="+phA, strings.NewReader(phA))
	response, err := r.client("coder", "tok", r.upstream.Certificate()).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body(t, response)
	if seen().body != phA || seen().rawQuery != "q="+phA {
		t.Errorf("a placeholder was swapped for another host: %+v", seen())
	}
}

func TestAnotherProfileIsUntouched(t *testing.T) {
	r, seen := swapRig(t, ph("secret"))
	r.proxy.SetPolicy(&Policy{Hash: "h", Profiles: map[string]Profile{
		"coder":    {Services: []Service{ph("secret")}},
		"reviewer": {Services: []Service{{Name: "docs", Hosts: []string{"example.com"}, Inspect: true}}},
	}})
	if err := r.proxy.BindAgent("reviewer", Binding{Token: "rtok", Profile: "reviewer"}); err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest("POST", "https://example.com/", strings.NewReader(phA))
	request.Header.Set("X-Site-Key", phA)
	response, err := r.client("reviewer", "rtok", r.ca.Cert).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body(t, response)
	if seen().body != phA || seen().header.Get("X-Site-Key") != phA {
		t.Errorf("swapped for a profile without the service: %+v", seen())
	}
}

func TestResponsesAreNeverSwapped(t *testing.T) {
	r, _ := swapRig(t, ph("secret"))
	response := do(t, r, "GET", "https://example.com/?a="+phA, "", nil)
	if got := body(t, response); got != "echo "+phA+" a=secret" {
		t.Errorf("response = %q", got)
	}
}

func TestAuditNeverHoldsASecretABodyOrAQuery(t *testing.T) {
	secret := "SUPER-SECRET-VALUE"
	r, _ := swapRig(t, ph(secret))
	body(t, do(t, r, "POST", "https://example.com/p/"+phA+"?q=hidden-query", "text/plain", strings.NewReader("hidden-body "+phA)))
	big := strings.Repeat("hidden-body", 1<<17)
	body(t, do(t, r, "POST", "https://example.com/p?q=hidden-query", "text/plain", strings.NewReader(big)))
	log := r.audit.String()
	for _, leak := range []string{secret, "hidden-query", "hidden-body"} {
		if strings.Contains(log, leak) {
			t.Errorf("the audit log holds %q:\n%s", leak, log)
		}
	}
	if !strings.Contains(log, `"action":"inject"`) || !strings.Contains(log, `"path":"/p/`+phA+`"`) {
		t.Errorf("audit should log the path as the agent wrote it:\n%s", log)
	}
}

func TestPlaceholderHostsAreInterceptedLikeInjectHosts(t *testing.T) {
	r, _ := swapRig(t, ph("secret"))
	body(t, do(t, r, "GET", "https://example.com/", "", nil))
	if !strings.Contains(r.audit.String(), `"action":"inject"`) {
		t.Errorf("audit:\n%s", r.audit.String())
	}
}

func TestDecideCollectsPlaceholdersFromEveryServiceOfTheHost(t *testing.T) {
	profile := Profile{Services: []Service{
		{Name: "a", Hosts: []string{"example.com"}, Placeholder: phA, Secret: "1"},
		{Name: "b", Hosts: []string{"*.example.com", "example.com"}, Placeholder: phB, Secret: "2"},
		{Name: "c", Hosts: []string{"unrelated.org"}, Placeholder: "egzo-ph-zzzz", Secret: "3"},
	}}
	decision := profile.Decide("example.com")
	if !decision.Allowed || decision.Service != "a" || decision.Inject != nil || len(decision.Substitutions) != 2 ||
		decision.Substitutions[0] != (Substitution{phA, "1"}) || decision.Substitutions[1] != (Substitution{phB, "2"}) {
		t.Errorf("decision = %+v", decision)
	}
	if got := profile.Decide("x.example.com"); len(got.Substitutions) != 1 || got.Substitutions[0].Secret != "2" {
		t.Errorf("wildcard decision = %+v", got)
	}
	if got := profile.Decide("nothing.net"); got.Allowed || len(got.Substitutions) != 0 {
		t.Errorf("decision = %+v", got)
	}
}

func TestJSONEscapeLeavesHTMLAlone(t *testing.T) {
	if got := jsonEscape(`<a href="x">&</a>`); got != `<a href=\"x\">&</a>` {
		t.Errorf("got %q", got)
	}
	var buf bytes.Buffer
	buf.WriteString(jsonEscape("tab\t☃\u2028"))
	if strings.ContainsAny(buf.String(), "\t") {
		t.Errorf("got %q", buf.String())
	}
}

// assertDeny checks that the audit log holds a deny event whose reason contains text.
func assertDeny(t *testing.T, r *rig, text string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(r.audit.String()), "\n") {
		var event Event
		if json.Unmarshal([]byte(line), &event) == nil && event.Action == "deny" && event.Host == "example.com" &&
			event.Agent == "coder" && strings.Contains(event.Reason, text) {
			return
		}
	}
	t.Errorf("no deny event with %q:\n%s", text, r.audit.String())
}

func TestARefusalNeverCarriesTheSecretBack(t *testing.T) {
	const secret = "REAL-SECRET-VALUE"
	cases := map[string]func(r *rig) *http.Response{
		"Content-Encoding": func(r *rig) *http.Response {
			return do(t, r, "POST", "https://example.com/", "text/plain", strings.NewReader("x"), "Content-Encoding", phA)
		},
		"other headers": func(r *rig) *http.Response {
			return do(t, r, "POST", "https://example.com/", "text/plain", strings.NewReader("x"),
				"Content-Encoding", "gzip", "X-Other", phA)
		},
		"url": func(r *rig) *http.Response {
			return do(t, r, "POST", "https://example.com/"+phA+"?k="+phA, "text/plain",
				strings.NewReader(strings.Repeat("a", 1<<20+1)), "X-Other", phA)
		},
	}
	for name, run := range cases {
		r, _ := swapRig(t, ph(secret))
		response := run(r)
		text := body(t, response)
		if response.StatusCode != http.StatusUnsupportedMediaType && response.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: status %d, %q", name, response.StatusCode, text)
		}
		if r.requests() != 0 {
			t.Errorf("%s: forwarded", name)
		}
		// The audit log keeps the path as the agent wrote it, so the token may be there; the secret never.
		if strings.Contains(text, secret) || strings.Contains(text, phA) || strings.Contains(r.audit.String(), secret) {
			t.Errorf("%s: a refusal holds the secret or the token:\n%s\n%s", name, text, r.audit.String())
		}
		if strings.Contains(r.audit.String(), `"reason":"`) && strings.Contains(strings.Join(auditReasons(r), ""), phA) {
			t.Errorf("%s: a reason holds the token", name)
		}
	}
}

func TestUpgradeHeaderWithoutConnectionUpgradeIsStillBodyChecked(t *testing.T) {
	r, _ := swapRig(t, ph("secret"))
	big := strings.Repeat("a", 1<<20+1)
	response := do(t, r, "POST", "https://example.com/", "text/plain", strings.NewReader(big), "Upgrade", "websocket")
	body(t, response)
	if response.StatusCode != http.StatusRequestEntityTooLarge || r.requests() != 0 {
		t.Errorf("status %d, forwarded %d", response.StatusCode, r.requests())
	}
	r, seen := swapRig(t, ph("secret"))
	body(t, do(t, r, "POST", "https://example.com/", "text/plain", strings.NewReader(phA), "Upgrade", "websocket"))
	if seen().body != "secret" {
		t.Errorf("body = %q", seen().body)
	}
}

func TestIsUpgradeNeedsTheConnectionToken(t *testing.T) {
	for header, want := range map[string]bool{"": false, "keep-alive": false, "Upgrade": true, "keep-alive, upgrade": true} {
		r, _ := http.NewRequest("POST", "http://x/", nil)
		r.Header.Set("Upgrade", "websocket")
		if header != "" {
			r.Header.Set("Connection", header)
		}
		if got := isUpgrade(r); got != want {
			t.Errorf("Connection %q: %v", header, got)
		}
	}
}

func auditReasons(r *rig) []string {
	var reasons []string
	for _, line := range strings.Split(r.audit.String(), "\n") {
		var event struct{ Reason string }
		if json.Unmarshal([]byte(line), &event) == nil && event.Reason != "" {
			reasons = append(reasons, event.Reason)
		}
	}
	return reasons
}
