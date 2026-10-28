// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// OAuth is the sign-in of an OAuth service: where its client exchanges a code and refreshes (TokenURL), and revokes.
type OAuth struct {
	TokenURL  string `json:"token_url"`
	RevokeURL string `json:"revoke_url,omitempty"`
}

const (
	// renewAhead is how long before the real access token expires the proxy renews it; renewEvery the longest it goes
	// without renewing (Codex refreshes a ChatGPT sign-in every 8 days, and so does the proxy, for any provider).
	renewAhead = 5 * time.Minute
	renewEvery = 8 * 24 * time.Hour
	// renewTick is how often the proxy looks at its sign-ins; renewGap the least time between two renewals of one, unless
	// its access token has expired (a provider that issues short-lived tokens is not asked at every request).
	renewTick = 10 * time.Second
	renewGap  = 30 * time.Second
	// standInMark starts what makes a stand-in: the signature of a JWT one, an opaque one whole. A stand-in is recognized
	// without the sign-in it came from, so one the proxy cannot serve is refused instead of passed on.
	standInMark = "egzo-"
	// keptStandIns is how many access stand-ins of a sign-in still work after newer ones were given out: the agents of
	// a project refresh at different times.
	keptStandIns = 16
	maxTokenBody = 1 << 20
)

// signIn is the sign-in of one service: the real tokens, which never leave the proxy, and the stand-ins its clients hold.
// It is kept sealed in the private volume.
type signIn struct {
	Service   string    `json:"service"`
	Access    string    `json:"access"`
	Refresh   string    `json:"refresh"`
	ID        string    `json:"id,omitempty"`
	Expires   time.Time `json:"expires"`
	Refreshed time.Time `json:"refreshed"`
	ClientID  string    `json:"client_id,omitempty"`
	// JSON says the client sends its token requests as JSON; the proxy renews the same way.
	JSON bool `json:"json,omitempty"`
	// Extra is what the provider's last answer said besides the tokens (scope, the account); every answer repeats it.
	Extra map[string]any `json:"extra,omitempty"`
	// Ended is why the sign-in no longer works (revoked, refused by the provider); its stand-ins get "sign in again".
	Ended string `json:"ended,omitempty"`

	StandInAccess  []string `json:"stand_in_access"` // newest last
	StandInRefresh string   `json:"stand_in_refresh"`
	StandInID      string   `json:"stand_in_id,omitempty"`
}

// tokens is a token endpoint's answer, as much of it as the proxy reads.
type tokens struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	ID        string `json:"id_token"`
	ExpiresIn int64  `json:"expires_in"`
	// Extra is the rest of the answer, which the client may read: Claude Code keeps a refresh only with its scope.
	Extra map[string]any `json:"-"`
}

// extras is an answer without its tokens.
func extras(whole map[string]any) map[string]any {
	rest := map[string]any{}
	for key, value := range whole {
		switch key {
		case "access_token", "refresh_token", "id_token", "expires_in":
		default:
			rest[key] = value
		}
	}
	return rest
}

// SignInStatus is what `project check` and the hub are told about the sign-in of a service: none, live or ended.
type SignInStatus struct {
	State   string    `json:"state"` // "none", "live", "ended"
	Ended   string    `json:"ended,omitempty"`
	Expires time.Time `json:"expires,omitempty"`
}

// signIns are the sign-ins of the project's OAuth services. Their records are sealed in the store; until the store is
// opened (a policy brings the secret key), none is known.
type signIns struct {
	mu      sync.Mutex
	store   *Store
	open    bool
	byName  map[string]*signIn
	now     func() time.Time
	refresh func(ctx context.Context, service string, oauth OAuth, form url.Values, asJSON bool) (*http.Response, error)
}

func newSignIns(store *Store) *signIns {
	return &signIns{store: store, byName: map[string]*signIn{}, now: time.Now}
}

// load reads the sealed sign-ins once the store is open.
func (s *signIns) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store == nil {
		return errLocked
	}
	records, err := s.store.SignIns()
	if err != nil {
		return err
	}
	s.byName = map[string]*signIn{}
	for _, data := range records {
		var record signIn
		if err := json.Unmarshal(data, &record); err == nil && record.Service != "" {
			s.byName[record.Service] = &record
		}
	}
	s.open = true
	return nil
}

// save seals a sign-in; it runs under s.mu, before the change it records is used.
func (s *signIns) save(record *signIn) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return s.store.SaveSignIn(record.Service, data)
}

func (s *signIns) status(service string) SignInStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.byName[service]
	switch {
	case record == nil:
		return SignInStatus{State: "none"}
	case record.Ended != "":
		return SignInStatus{State: "ended", Ended: record.Ended}
	}
	return SignInStatus{State: "live", Expires: record.Expires}
}

// standIn makes a stand-in for a real token: a JWT keeps its header and claims, which clients read, and gets a
// signature that is no signature; anything else becomes a random opaque value. Both carry standInMark.
func standIn(real string) string {
	random := make([]byte, 48)
	rand.Read(random)
	parts := strings.Split(real, ".")
	if len(parts) == 3 && parts[0] != "" && parts[1] != "" {
		return parts[0] + "." + parts[1] + "." + standInMark + base64.RawURLEncoding.EncodeToString(random)
	}
	return standInMark + hex.EncodeToString(random)
}

// isStandIn reports whether a token is one the proxy made.
func isStandIn(token string) bool {
	if strings.HasPrefix(token, standInMark) {
		return true
	}
	parts := strings.Split(token, ".")
	return len(parts) == 3 && strings.HasPrefix(parts[2], standInMark)
}

// capture records the tokens of a sign-in the provider just issued, and answers with stand-ins. asJSON says the client
// sent the exchange as JSON.
func (s *signIns) capture(service, clientID string, asJSON bool, issued tokens) (tokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return tokens{}, errLocked
	}
	now := s.now()
	record := &signIn{
		Service: service, Access: issued.Access, Refresh: issued.Refresh, ID: issued.ID,
		Expires: expiry(now, issued.ExpiresIn, issued.Access), Refreshed: now, ClientID: clientID, JSON: asJSON, Extra: issued.Extra,
		StandInAccess: []string{standIn(issued.Access)}, StandInRefresh: standIn(issued.Refresh),
	}
	if issued.ID != "" {
		record.StandInID = standIn(issued.ID)
	}
	if err := s.save(record); err != nil {
		return tokens{}, err
	}
	s.byName[service] = record
	return record.answer(now), nil
}

// answer is what a client is told after a sign-in or a refresh: the stand-ins of the current tokens.
func (r *signIn) answer(now time.Time) tokens {
	expiresIn := int64(r.Expires.Sub(now) / time.Second)
	if expiresIn < 0 {
		expiresIn = 0
	}
	return tokens{Access: r.StandInAccess[len(r.StandInAccess)-1], Refresh: r.StandInRefresh, ID: r.StandInID, ExpiresIn: expiresIn, Extra: r.Extra}
}

// expiry is when an access token expires: expires_in when the provider said, else the JWT's exp claim, else in an hour.
func expiry(now time.Time, expiresIn int64, access string) time.Time {
	if expiresIn > 0 {
		return now.Add(time.Duration(expiresIn) * time.Second)
	}
	if parts := strings.Split(access, "."); len(parts) == 3 {
		if payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "=")); err == nil {
			var claims struct {
				Exp int64 `json:"exp"`
			}
			if json.Unmarshal(payload, &claims) == nil && claims.Exp > 0 {
				return time.Unix(claims.Exp, 0)
			}
		}
	}
	return now.Add(time.Hour)
}

var (
	errLocked   = errors.New("the proxy has not been given the secret key since it started: run `egzo project up`")
	errNoSignIn = errors.New("no sign-in")
)

// access is the real access token for a stand-in a client sent, renewed first when it is about to expire. A stand-in
// of an ended sign-in, or one the proxy no longer knows, is errNoSignIn.
func (s *signIns) access(ctx context.Context, service string, oauth OAuth, stand string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return "", errLocked
	}
	record := s.byName[service]
	if record == nil || record.Ended != "" || !slices.Contains(record.StandInAccess, stand) {
		return "", errNoSignIn
	}
	if s.due(record) {
		s.renew(ctx, record, oauth) // a failure keeps the token it has, or ends the sign-in
		if record.Ended != "" {
			return "", errNoSignIn
		}
	}
	return record.Access, nil
}

// refreshFor answers a client's refresh: the current stand-ins, renewing the real tokens first if they need it.
func (s *signIns) refreshFor(ctx context.Context, service string, oauth OAuth, stand string) (tokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return tokens{}, errLocked
	}
	record := s.byName[service]
	if record == nil || record.Ended != "" || record.StandInRefresh != stand {
		return tokens{}, errNoSignIn
	}
	if s.due(record) {
		if s.renew(ctx, record, oauth); record.Ended != "" {
			return tokens{}, errNoSignIn
		}
	}
	return record.answer(s.now()), nil
}

// renew refreshes the real tokens of a sign-in at the provider. It runs under s.mu. The rotated refresh token is sealed
// before the new access token is used. A refusal (invalid_grant: revoked, reused, expired) ends the sign-in; anything
// else (the network, a server error) keeps the tokens for the next try.
func (s *signIns) renew(ctx context.Context, record *signIn, oauth OAuth) {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {record.Refresh}}
	if record.ClientID != "" {
		form.Set("client_id", record.ClientID)
	}
	response, err := s.refresh(ctx, record.Service, oauth, form, record.JSON)
	if err != nil {
		return
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxTokenBody))
	var issued tokens
	var whole map[string]any
	if response.StatusCode == http.StatusOK && json.Unmarshal(body, &issued) == nil && json.Unmarshal(body, &whole) == nil && issued.Access != "" {
		next := *record
		// What a refresh leaves out (often the scope) stays as the sign-in said it.
		next.Extra = maps.Clone(record.Extra)
		if next.Extra == nil {
			next.Extra = map[string]any{}
		}
		maps.Copy(next.Extra, extras(whole))
		next.Access, next.Expires, next.Refreshed = issued.Access, expiry(s.now(), issued.ExpiresIn, issued.Access), s.now()
		if issued.Refresh != "" {
			next.Refresh = issued.Refresh
		}
		if issued.ID != "" {
			next.ID = issued.ID
			next.StandInID = standIn(issued.ID)
		}
		next.StandInAccess = append(slices.Clone(record.StandInAccess), standIn(issued.Access))
		if len(next.StandInAccess) > keptStandIns {
			next.StandInAccess = next.StandInAccess[len(next.StandInAccess)-keptStandIns:]
		}
		if s.save(&next) == nil {
			*record = next
		}
		return
	}
	var refused struct {
		Error string `json:"error"`
	}
	if (response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnauthorized) &&
		json.Unmarshal(body, &refused) == nil && refused.Error == "invalid_grant" {
		s.end(record, "the provider refused to renew it (revoked, signed out elsewhere, or expired)")
	}
}

// end marks a sign-in ended: its real tokens are dropped, its stand-ins are kept to answer "sign in again".
func (s *signIns) end(record *signIn, why string) {
	record.Ended, record.Access, record.Refresh, record.ID = why, "", "", ""
	s.save(record)
}

// due reports whether a sign-in's access token should be renewed now.
func (s *signIns) due(record *signIn) bool {
	now := s.now()
	if !now.Before(record.Expires) {
		return true
	}
	if now.Sub(record.Refreshed) < renewGap {
		return false
	}
	return now.Add(renewAhead).After(record.Expires) || now.Sub(record.Refreshed) > renewEvery
}

// renewDue renews every live sign-in whose access token expires soon or that was not renewed for renewEvery.
func (s *signIns) renewDue(ctx context.Context, services map[string]OAuth) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return
	}
	for name, record := range s.byName {
		oauth, ok := services[name]
		if !ok || record.Ended != "" {
			continue
		}
		if s.due(record) {
			s.renew(ctx, record, oauth)
		}
	}
}

// revoke ends the sign-in a stand-in belongs to, revoking its real refresh token at the provider when it has a revoke
// URL. It reports false when the token is not a stand-in of the sign-in.
func (s *signIns) revoke(ctx context.Context, service string, oauth OAuth, stand string, send func(*http.Request) (*http.Response, error)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.byName[service]
	if record == nil || (record.StandInRefresh != stand && !slices.Contains(record.StandInAccess, stand)) {
		return false
	}
	if record.Ended == "" && oauth.RevokeURL != "" && record.Refresh != "" {
		form := url.Values{"token": {record.Refresh}, "token_type_hint": {"refresh_token"}}
		if record.ClientID != "" {
			form.Set("client_id", record.ClientID)
		}
		if request, err := http.NewRequestWithContext(ctx, http.MethodPost, oauth.RevokeURL, strings.NewReader(form.Encode())); err == nil {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if response, err := send(request); err == nil {
				response.Body.Close()
			}
		}
	}
	s.end(record, "revoked")
	return true
}

// logout ends the sign-in of a service, from the operator (`egzo proxy logout`).
func (s *signIns) logout(ctx context.Context, service string, oauth OAuth, send func(*http.Request) (*http.Response, error)) bool {
	s.mu.Lock()
	record := s.byName[service]
	s.mu.Unlock()
	if record == nil || record.Ended != "" {
		return false
	}
	return s.revoke(ctx, service, oauth, record.StandInRefresh, send)
}

// --- the token endpoint -------------------------------------------------------------------------------------------

// tokenBody is the body of a request to a token or revoke endpoint, form or JSON, read once and kept to forward.
type tokenBody struct {
	raw    []byte
	json   bool
	values map[string]string
}

// errCoded is a token request whose body is compressed: the proxy has to read it, so it is refused with 415.
var errCoded = errors.New("a compressed body (Content-Encoding) cannot be read: send it uncompressed")

func readTokenBody(r *http.Request) (*tokenBody, error) {
	if contentCoded(r) {
		return nil, errCoded
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxTokenBody+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxTokenBody {
		return nil, errors.New("the body is too large")
	}
	body := &tokenBody{raw: raw, values: map[string]string{}}
	if strings.Contains(r.Header.Get("Content-Type"), "json") {
		body.json = true
		var values map[string]any
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, err
		}
		for key, value := range values {
			if text, ok := value.(string); ok {
				body.values[key] = text
			}
		}
		return body, nil
	}
	form, err := url.ParseQuery(string(raw))
	if err != nil {
		return nil, err
	}
	for key := range form {
		body.values[key] = form.Get(key)
	}
	return body, nil
}

// oauthError answers like a token endpoint that refuses: what an OAuth client understands.
func oauthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": description})
}

// serveToken answers a request to the token URL of an OAuth service. A refresh with a stand-in is answered here;
// any other grant goes to the provider, and the tokens of its answer are captured and replaced by stand-ins, so the
// agent never gets a real token through the proxy. A stand-in is never sent to the provider.
func (s *Server) serveToken(w http.ResponseWriter, r *http.Request, service string, oauth OAuth) (status int) {
	body, err := readTokenBody(r)
	if errors.Is(err, errCoded) {
		acceptIdentityOnly(w)
		oauthError(w, http.StatusUnsupportedMediaType, "invalid_request", "egzo: "+err.Error())
		return http.StatusUnsupportedMediaType
	}
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "egzo: the body cannot be read: "+err.Error())
		return http.StatusBadRequest
	}
	grant := body.values["grant_type"]
	if grant == "refresh_token" && isStandIn(body.values["refresh_token"]) {
		answer, err := s.signIns.refreshFor(r.Context(), service, oauth, body.values["refresh_token"])
		switch {
		case errors.Is(err, errLocked):
			oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "egzo: "+err.Error())
			return http.StatusServiceUnavailable
		case err != nil:
			oauthError(w, http.StatusBadRequest, "invalid_grant", "egzo: the sign-in of service "+service+" ended: sign in again")
			return http.StatusBadRequest
		}
		writeTokens(w, answer)
		return http.StatusOK
	}
	for _, value := range body.values {
		if isStandIn(value) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "egzo: a stand-in token only works through this proxy, for a refresh")
			return http.StatusBadRequest
		}
	}
	response, err := s.forward(r, body.raw)
	if err != nil {
		oauthError(w, http.StatusBadGateway, "temporarily_unavailable", "egzo: upstream error")
		return http.StatusBadGateway
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, maxTokenBody))
	var issued tokens
	var whole map[string]any
	understood := json.Unmarshal(data, &issued) == nil && json.Unmarshal(data, &whole) == nil
	if !understood && response.StatusCode/100 == 2 {
		// It may hold tokens the proxy cannot see: it does not reach the client.
		oauthError(w, http.StatusBadGateway, "temporarily_unavailable", "egzo: the provider's answer is not understood")
		return http.StatusBadGateway
	}
	if !understood || (issued.Access == "" && issued.Refresh == "" && issued.ID == "") {
		copyHeaders(w, response)
		w.WriteHeader(response.StatusCode)
		w.Write(data)
		return response.StatusCode
	}
	issued.Extra = extras(whole)
	answer, err := s.signIns.capture(service, body.values["client_id"], body.json, issued)
	if err != nil {
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "egzo: the sign-in could not be kept: "+err.Error())
		return http.StatusServiceUnavailable
	}
	writeTokens(w, answer)
	return http.StatusOK
}

// serveRevoke answers a request to the revoke URL: a stand-in revokes the real token and signs the project out.
func (s *Server) serveRevoke(w http.ResponseWriter, r *http.Request, service string, oauth OAuth) int {
	body, err := readTokenBody(r)
	if errors.Is(err, errCoded) {
		acceptIdentityOnly(w)
		oauthError(w, http.StatusUnsupportedMediaType, "invalid_request", "egzo: "+err.Error())
		return http.StatusUnsupportedMediaType
	}
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "egzo: the body cannot be read: "+err.Error())
		return http.StatusBadRequest
	}
	token := body.values["token"]
	if isStandIn(token) {
		s.signIns.revoke(r.Context(), service, oauth, token, s.transport().RoundTrip)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{}")
		return http.StatusOK
	}
	response, err := s.forward(r, body.raw)
	if err != nil {
		http.Error(w, "egzo: upstream error", http.StatusBadGateway)
		return http.StatusBadGateway
	}
	defer response.Body.Close()
	copyHeaders(w, response)
	w.WriteHeader(response.StatusCode)
	io.Copy(w, io.LimitReader(response.Body, maxTokenBody))
	return response.StatusCode
}

// writeTokens answers with stand-ins: the provider's own answer with its tokens replaced, or a token answer of its own.
func writeTokens(w http.ResponseWriter, answer tokens) {
	whole := maps.Clone(answer.Extra)
	if whole == nil {
		whole = map[string]any{}
	}
	if _, ok := whole["token_type"]; !ok {
		whole["token_type"] = "Bearer"
	}
	whole["access_token"], whole["refresh_token"], whole["expires_in"] = answer.Access, answer.Refresh, answer.ExpiresIn
	if answer.ID != "" {
		whole["id_token"] = answer.ID
	} else {
		delete(whole, "id_token")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(whole)
}

// forward sends a request the proxy read the body of to the upstream it was meant for. The client's Accept-Encoding is
// not sent: the transport then asks for compression itself and decodes the answer, which the proxy has to read.
func (s *Server) forward(r *http.Request, body []byte) (*http.Response, error) {
	out, err := http.NewRequestWithContext(r.Context(), r.Method, "https://"+r.Host+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for name, values := range r.Header {
		if !strings.EqualFold(name, "Proxy-Authorization") && !strings.EqualFold(name, "Accept-Encoding") {
			out.Header[name] = values
		}
	}
	return s.transport().RoundTrip(out)
}

func copyHeaders(w http.ResponseWriter, response *http.Response) {
	for _, name := range []string{"Content-Type", "Cache-Control"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
}

// refreshUpstream is how the sign-ins renew at the provider: a standard refresh grant, through the policy's upstream,
// form encoded or as JSON, as the client sent it (some providers take only one).
func (s *Server) refreshUpstream(ctx context.Context, service string, oauth OAuth, form url.Values, asJSON bool) (*http.Response, error) {
	body, contentType := form.Encode(), "application/x-www-form-urlencoded"
	if asJSON {
		values := map[string]string{}
		for key := range form {
			values[key] = form.Get(key)
		}
		data, err := json.Marshal(values)
		if err != nil {
			return nil, err
		}
		body, contentType = string(data), "application/json"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, oauth.TokenURL, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/json")
	return s.transport().RoundTrip(request)
}

// oauthServices are the OAuth services of the loaded policy, by name.
func (s *Server) oauthServices() map[string]OAuth {
	services := map[string]OAuth{}
	if policy := s.policy.Load(); policy != nil {
		for _, profile := range policy.Profiles {
			for _, service := range profile.Services {
				if service.OAuth != nil {
					services[service.Name] = *service.OAuth
				}
			}
		}
	}
	return services
}

// RenewSignIns renews the sign-ins that need it, every renewTick, until ctx ends: the chains stay alive while no agent
// runs, and one proxy per project means one renewer.
func (s *Server) RenewSignIns(ctx context.Context) {
	ticker := time.NewTicker(renewTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.signIns.renewDue(ctx, s.oauthServices())
		}
	}
}

// signInError answers a request whose stand-in the proxy cannot serve, in the shape of an API error.
func signInError(w http.ResponseWriter, service string, err error) {
	status, message := http.StatusUnauthorized, fmt.Sprintf("egzo: the sign-in of service %s ended or was replaced: sign in again", service)
	if errors.Is(err, errLocked) {
		status, message = http.StatusServiceUnavailable, "egzo: "+err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": message, "type": "invalid_request_error", "code": "token_expired"}})
}
