// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func (r *rig) project(agents ...string) {
	r.t.Helper()
	for _, agent := range agents {
		r.register(agent)
	}
}

// request is an operator request to an agent; it returns the message id.
func (r *rig) request(to, text string) string {
	r.t.Helper()
	response := r.asOperator("POST", "/v1/messages", `{"to":"agent:`+to+`","text":"`+text+`"}`)
	if response.StatusCode != 200 {
		r.t.Fatalf("send: status %d", response.StatusCode)
	}
	var queued struct{ ID string }
	decode(r.t, response, &queued)
	return queued.ID
}

func statusOf(t *testing.T, err error) int {
	t.Helper()
	var failure *apiError
	if err == nil {
		return 0
	}
	if e, ok := err.(*apiError); ok {
		failure = e
	} else {
		t.Fatalf("not an apiError: %v", err)
	}
	return failure.Status
}

func TestARequestIsQueuedWithAnUnguessableIdAndItsKind(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	id := r.request("coder", "do it")
	if !strings.HasPrefix(id, "m") || len(id) != 33 {
		t.Errorf("id = %q: want m and 32 hex characters", id)
	}
	m, _ := r.srv.events.message(id)
	if m.From != "operator" || m.To != "agent:coder" || m.Kind != kindRequest || m.State != stateQueued || m.Text != "do it" {
		t.Errorf("message = %+v", m)
	}
	if other := r.request("coder", "again"); other == id {
		t.Error("two messages share an id")
	}
}

func TestFetchMarksFetchedOnceAndAnAgentSeesOnlyItsOwnMessages(t *testing.T) {
	r := newRig(t)
	r.project("coder", "reviewer")
	id := r.request("coder", "private")
	got, err := r.srv.fetch("coder", id)
	if err != nil || got.State != stateFetched || got.Text != "private" {
		t.Fatalf("fetch = %+v, %v", got, err)
	}
	r.srv.fetch("coder", id)
	fetches := 0
	for _, e := range r.srv.events.all() {
		if e.Type == "fetched" {
			fetches++
		}
	}
	if fetches != 1 {
		t.Errorf("%d fetched events, want 1", fetches)
	}
	_, foreign := r.srv.fetch("reviewer", id)
	_, unknown := r.srv.fetch("reviewer", "m"+strings.Repeat("0", 32))
	if foreign == nil || unknown == nil || foreign.Error() != unknown.Error() || statusOf(t, foreign) != http.StatusNotFound {
		t.Errorf("a foreign message and an unknown one must look alike: %v / %v", foreign, unknown)
	}
}

func TestResolveNeedsAFetchedRequestAValidOutcomeAndHappensOnce(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	id := r.request("coder", "do it")
	_, err := r.srv.resolve("coder", id, "ok", "done")
	if statusOf(t, err) != http.StatusConflict || !strings.Contains(err.Error(), "fetch") {
		t.Errorf("resolving before fetching: %v", err)
	}
	r.srv.fetch("coder", id)
	if _, err := r.srv.resolve("coder", id, "ok", "perhaps"); statusOf(t, err) != http.StatusBadRequest {
		t.Errorf("a bad outcome: %v", err)
	}
	if _, err := r.srv.resolve("coder", id, "  ", "done"); statusOf(t, err) != http.StatusBadRequest {
		t.Errorf("empty text: %v", err)
	}
	resolution, err := r.srv.resolve("coder", id, "all green", "done")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := r.srv.events.message(id)
	if m.State != stateResolved || m.Outcome != "done" {
		t.Errorf("message = %+v", m)
	}
	reply, _ := r.srv.events.message(resolution)
	if reply.Kind != kindResolution || reply.To != "operator" || reply.From != "agent:coder" || reply.Re != id || reply.Text != "all green" || reply.Outcome != "done" {
		t.Errorf("resolution = %+v", reply)
	}
	if _, err := r.srv.resolve("coder", id, "again", "done"); statusOf(t, err) != http.StatusConflict || !strings.Contains(err.Error(), "resolved") {
		t.Errorf("a second resolution: %v", err)
	}
}

func TestOnlyRequestsAndQuestionsCanBeResolved(t *testing.T) {
	r := newRig(t)
	r.project("coder", "reviewer")
	sent, _ := r.srv.sendFromAgent("coder", "agent:reviewer", "review this", "")
	r.srv.fetch("reviewer", sent)
	resolution, _ := r.srv.resolve("reviewer", sent, "fine", "done")
	r.srv.fetch("coder", resolution)
	if _, err := r.srv.resolve("coder", resolution, "thanks", "done"); statusOf(t, err) != http.StatusBadRequest {
		t.Errorf("resolving a resolution: %v", err)
	}
}

func TestUpdateIsAPassiveMessageToTheRequester(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	id := r.request("coder", "long job")
	if _, err := r.srv.update("coder", id, "early"); statusOf(t, err) != http.StatusConflict {
		t.Errorf("an update before fetching: %v", err)
	}
	r.srv.fetch("coder", id)
	updateID, err := r.srv.update("coder", id, "halfway")
	if err != nil {
		t.Fatal(err)
	}
	update, _ := r.srv.events.message(updateID)
	if update.Kind != kindUpdate || update.To != "operator" || update.Re != id || update.Text != "halfway" {
		t.Errorf("update = %+v", update)
	}
	if m, _ := r.srv.events.message(id); m.State != stateFetched {
		t.Errorf("an update changed the request to %s", m.State)
	}
}

func TestAskQuestionsTheRequesterAndLeavesTheRequestOpen(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	id := r.request("coder", "deploy")
	r.srv.fetch("coder", id)
	questionID, err := r.srv.ask("coder", id, "to prod?", []string{"yes", "no"})
	if err != nil {
		t.Fatal(err)
	}
	question, _ := r.srv.events.message(questionID)
	if question.Kind != kindQuestion || question.To != "operator" || question.From != "agent:coder" || question.Re != id || len(question.Choices) != 2 {
		t.Errorf("question = %+v", question)
	}
	if m, _ := r.srv.events.message(id); m.State != stateFetched {
		t.Errorf("asking closed the request: %s", m.State)
	}
	if open, waiting := r.srv.overlay("coder"); open != 1 || !waiting {
		t.Errorf("overlay = %d, %v", open, waiting)
	}
	answer, err := r.srv.resolveForPerson("operator", questionID, "yes", "done")
	if err != nil {
		t.Fatal(err)
	}
	reply, _ := r.srv.events.message(answer)
	if reply.To != "agent:coder" || reply.Re != questionID || reply.From != "operator" {
		t.Errorf("answer = %+v", reply)
	}
	if open, waiting := r.srv.overlay("coder"); open != 1 || waiting {
		t.Errorf("overlay after the answer = %d, %v", open, waiting)
	}
	if _, err := r.srv.resolveForPerson("operator", questionID, "again", "done"); statusOf(t, err) != http.StatusConflict {
		t.Errorf("answering twice: %v", err)
	}
}

func TestAPersonCanOnlyResolveWhatIsAddressedToAPerson(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	id := r.request("coder", "mine to do")
	if _, err := r.srv.resolveForPerson("operator", id, "I did it", "done"); statusOf(t, err) != http.StatusBadRequest || !strings.Contains(err.Error(), "agent:coder") {
		t.Errorf("err = %v", err)
	}
	if _, err := r.srv.resolveForPerson("operator", "m"+strings.Repeat("0", 32), "x", "done"); statusOf(t, err) != http.StatusNotFound {
		t.Errorf("an unknown id: %v", err)
	}
}

func TestMessagesBetweenAgentsNeedARealRecipientAndAnAddress(t *testing.T) {
	r := newRig(t)
	r.project("coder", "reviewer")
	if _, err := r.srv.sendFromAgent("coder", "agent:reviewer", "hello", ""); err != nil {
		t.Fatal(err)
	}
	for to, want := range map[string]int{"agent:ghost": 404, "agent:coder": 400, "reviewer": 400, "": 400, "nobody": 400} {
		if _, err := r.srv.sendFromAgent("coder", to, "x", ""); statusOf(t, err) != want {
			t.Errorf("to %q: %v, want status %d", to, err, want)
		}
	}
	if _, err := r.srv.sendFromAgent("coder", "operator", "report", ""); err != nil {
		t.Errorf("a message to the operator: %v", err)
	}
	if _, err := r.srv.sendFromAgent("coder", "user:alice", "hello", ""); err != nil {
		t.Errorf("a message to a user: %v", err)
	}
}

func TestAThreadIsThreadedMustBelongToTheSenderAndHasADepthLimit(t *testing.T) {
	r := newRig(t)
	r.project("coder", "reviewer")
	root := r.request("coder", "start")
	child, err := r.srv.sendFromAgent("coder", "agent:reviewer", "child", root)
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := r.srv.events.message(child); m.Re != root || m.Hops != 1 {
		t.Errorf("child = %+v", m)
	}
	if _, err := r.srv.sendFromAgent("coder", "agent:reviewer", "x", "m"+strings.Repeat("0", 32)); statusOf(t, err) != http.StatusNotFound {
		t.Errorf("an unknown parent: %v", err)
	}
	if _, err := r.srv.sendFromAgent("reviewer", "agent:coder", "x", root); statusOf(t, err) != http.StatusNotFound {
		t.Errorf("a thread the sender is not part of: %v", err)
	}
	current, sender, receiver := child, "reviewer", "coder"
	for hop := 2; hop < maxThreadDepth; hop++ {
		next, err := r.srv.sendFromAgent(sender, "agent:"+receiver, "hop", current)
		if err != nil {
			t.Fatalf("hop %d: %v", hop, err)
		}
		current, sender, receiver = next, receiver, sender
	}
	_, err = r.srv.sendFromAgent(sender, "agent:"+receiver, "one too many", current)
	if statusOf(t, err) != http.StatusBadRequest || !strings.Contains(err.Error(), "deep") {
		t.Errorf("past the depth limit: %v", err)
	}
}

func TestARecipientHasAtMostTwentyOpenRequests(t *testing.T) {
	r := newRig(t)
	r.project("coder", "reviewer")
	for n := 0; n < maxOpenPerRecipient; n++ {
		if _, err := r.srv.sendFromAgent("coder", "agent:reviewer", "request", ""); err != nil {
			t.Fatalf("request %d: %v", n, err)
		}
	}
	_, err := r.srv.sendFromAgent("coder", "agent:reviewer", "one too many", "")
	if statusOf(t, err) != http.StatusConflict || !strings.Contains(err.Error(), "open") {
		t.Errorf("err = %v", err)
	}
	r.srv.fetch("reviewer", r.firstOpen("agent:reviewer"))
	r.srv.resolve("reviewer", r.firstOpen("agent:reviewer"), "done", "done")
	if _, err := r.srv.sendFromAgent("coder", "agent:reviewer", "room again", ""); err == nil {
		t.Log("room made by a resolution is only freed once it is resolved; the sender's rate is also limited")
	}
}

func (r *rig) firstOpen(to string) string {
	for _, m := range r.srv.events.selectMessages(func(m Message) bool { return m.To == to && m.State != stateResolved && m.Kind == kindRequest }) {
		return m.ID
	}
	return ""
}

func TestAnAgentMayNotSendMoreThanThirtyMessagesAMinuteButAPersonMay(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	for n := 0; n < maxSendsPerMinute; n++ {
		if _, err := r.srv.sendFromAgent("coder", fmt.Sprintf("user:u%d", n%10), "note", ""); err != nil {
			t.Fatalf("message %d: %v", n, err)
		}
	}
	_, err := r.srv.sendFromAgent("coder", "operator", "one too many", "")
	if statusOf(t, err) != http.StatusTooManyRequests || !strings.Contains(err.Error(), "minute") {
		t.Errorf("err = %v", err)
	}
	if _, err := r.srv.sendFromAgent("coder", "operator", "later", ""); err == nil {
		t.Error("the limit lifted without time passing")
	}
	// the window slides
	r.srv.rateMu.Lock()
	old := time.Now().Add(-2 * time.Minute)
	for i := range r.srv.sends["agent:coder"] {
		r.srv.sends["agent:coder"][i] = old
	}
	r.srv.rateMu.Unlock()
	if _, err := r.srv.sendFromAgent("coder", "operator", "a minute later", ""); err != nil {
		t.Errorf("after the window: %v", err)
	}
	for n := 0; n < 3*maxSendsPerMinute; n++ {
		if err := r.srv.rateLimit("operator", time.Now()); err != nil {
			t.Fatalf("a person at the CLI was rate limited: %v", err)
		}
	}
}

func TestListMessagesShowsOnlyAnAgentsOpenAnnouncedOrFetchedItems(t *testing.T) {
	r := newRig(t)
	r.project("coder", "reviewer")
	queued := r.request("coder", "not announced yet")
	fetched := r.request("coder", "fetched")
	r.srv.fetch("coder", fetched)
	resolved := r.request("coder", "done already")
	r.srv.fetch("coder", resolved)
	r.srv.resolve("coder", resolved, "ok", "done")
	r.request("reviewer", "someone else's")
	var ids []string
	for _, m := range r.srv.listFor("coder") {
		ids = append(ids, m.ID)
	}
	if len(ids) != 1 || ids[0] != fetched {
		t.Errorf("list = %v, want only %s (not %s queued, not %s resolved)", ids, fetched, queued, resolved)
	}
}

func TestOverlayCountsFetchedRequestsAndWaitingQuestions(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	if open, waiting := r.srv.overlay("coder"); open != 0 || waiting {
		t.Errorf("overlay = %d, %v", open, waiting)
	}
	a, b := r.request("coder", "a"), r.request("coder", "b")
	r.srv.fetch("coder", a)
	if open, _ := r.srv.overlay("coder"); open != 1 {
		t.Errorf("open = %d, want 1 (b is not fetched)", open)
	}
	r.srv.fetch("coder", b)
	if open, _ := r.srv.overlay("coder"); open != 2 {
		t.Errorf("open = %d, want 2", open)
	}
	r.srv.resolve("coder", a, "ok", "done")
	if open, _ := r.srv.overlay("coder"); open != 1 {
		t.Errorf("open = %d, want 1", open)
	}
}

func TestAgentsListsTheOthersWithTheirOverlay(t *testing.T) {
	r := newRig(t)
	r.project("coder", "reviewer")
	id := r.request("reviewer", "look")
	r.srv.fetch("reviewer", id)
	others := r.srv.othersOf("coder")
	if len(others) != 1 || others[0].Agent != "reviewer" || others[0].Open != 1 {
		t.Errorf("others = %+v", others)
	}
	all := r.srv.agentStatuses()
	if len(all) != 2 {
		t.Errorf("all = %+v", all)
	}
}

func TestOperatorMessagesCanBeListedFilteredAndFetchedByID(t *testing.T) {
	r := newRig(t)
	r.project("coder", "reviewer")
	id := r.request("coder", "for the coder")
	r.request("reviewer", "for the reviewer")
	r.srv.fetch("coder", id)
	r.srv.resolve("coder", id, "done", "done")

	list := func(query string) []Message {
		response := r.asOperator("GET", "/v1/messages?"+query, "")
		var messages []Message
		decode(t, response, &messages)
		return messages
	}
	if open := list(""); len(open) != 1 || open[0].Text != "for the reviewer" {
		t.Errorf("open = %+v", open)
	}
	if all := list("all=1"); len(all) != 3 { // two requests and one resolution
		t.Errorf("all = %+v", all)
	}
	if coder := list("all=1&agent=coder"); len(coder) != 2 {
		t.Errorf("coder = %+v", coder)
	}
	if kinds := list("all=1&kind=resolution"); len(kinds) != 1 || kinds[0].To != "operator" {
		t.Errorf("resolutions = %+v", kinds)
	}
	response := r.asOperator("GET", "/v1/messages/"+id, "")
	var one Message
	decode(t, response, &one)
	if one.ID != id || one.State != stateResolved {
		t.Errorf("one = %+v", one)
	}
	if missing := r.asOperator("GET", "/v1/messages/m"+strings.Repeat("0", 32), ""); missing.StatusCode != http.StatusNotFound {
		t.Errorf("missing = %d", missing.StatusCode)
	}
}

func TestTheOperatorAPIRefusesUnknownAgentsBadSendersAndAgentsAsSenders(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	for body, want := range map[string]int{
		`{"to":"agent:ghost","text":"x"}`:                         404,
		`{"to":"coder","text":"x"}`:                               400,
		`{"to":"agent:coder","from":"agent:reviewer","text":"x"}`: 400,
		`{"to":"agent:coder","from":"root","text":"x"}`:           400,
		`{"to":"agent:coder","text":" "}`:                         400,
		`not json`:                                                400,
	} {
		response := r.asOperator("POST", "/v1/messages", body)
		response.Body.Close()
		if response.StatusCode != want {
			t.Errorf("%s: status %d, want %d", body, response.StatusCode, want)
		}
	}
	response := r.asOperator("POST", "/v1/messages", `{"to":"agent:coder","from":"user:alice","text":"hi"}`)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Errorf("a user as sender: %d", response.StatusCode)
	}
}

func TestTheMessageViewSurvivesARestartOfTheSidecar(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	id := r.request("coder", "remember")
	r.srv.fetch("coder", id)
	reopened, err := newServer(r.srv.dir)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := reopened.events.message(id)
	if !ok || m.State != stateFetched || m.Text != "remember" {
		t.Errorf("message after restart = %+v, %v", m, ok)
	}
}

func TestResolutionsGoToWhoeverSentTheRequestAndCanBeFetchedByThem(t *testing.T) {
	r := newRig(t)
	r.project("coder", "reviewer")
	sent, _ := r.srv.sendFromAgent("coder", "agent:reviewer", "review", "")
	r.srv.fetch("reviewer", sent)
	resolution, _ := r.srv.resolve("reviewer", sent, "lgtm", "done")
	got, err := r.srv.fetch("coder", resolution)
	if err != nil || got.Kind != kindResolution || got.Text != "lgtm" || got.Outcome != "done" {
		t.Fatalf("fetch = %+v, %v", got, err)
	}
	if _, err := r.srv.fetch("reviewer", resolution); err == nil {
		t.Error("the resolver fetched the reply meant for the sender")
	}
}

func TestEventsOfAConversationAreFiledUnderTheAgentInvolved(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	id := r.request("coder", "go")
	r.srv.fetch("coder", id)
	r.srv.update("coder", id, "going")
	r.srv.resolve("coder", id, "gone", "done")
	byType := map[string]string{}
	for _, e := range r.srv.events.all() {
		byType[e.Type] = e.Agent
	}
	for _, kind := range []string{"message", "fetched", "resolved"} {
		if byType[kind] != "coder" {
			t.Errorf("%s event filed under %q, want coder", kind, byType[kind])
		}
	}
}

func TestASenderSeesWhereWhatItSentStandsAndItsLatestUpdateUntilItIsResolved(t *testing.T) {
	r := newRig(t)
	r.project("lead", "helper")
	sent := func() []Sent {
		var out []Sent
		decode(t, r.asAgent("lead", "GET", "/v1/messages?sent=1", ""), &out)
		return out
	}
	response := r.asAgent("lead", "POST", "/v1/messages", `{"to":"agent:helper","text":"count the files"}`)
	var created struct{ ID string }
	decode(t, response, &created)
	if got := sent(); len(got) != 1 || got[0].ID != created.ID || got[0].State != stateQueued || got[0].LatestUpdate != nil {
		t.Fatalf("right after sending: %+v", got)
	}
	r.asAgent("helper", "GET", "/v1/messages/"+created.ID, "").Body.Close()
	r.asAgent("helper", "POST", "/v1/messages/"+created.ID+"/update", `{"text":"first half"}`).Body.Close()
	r.asAgent("helper", "POST", "/v1/messages/"+created.ID+"/update", `{"text":"counting now"}`).Body.Close()
	got := sent()
	if len(got) != 1 || got[0].State != stateFetched || got[0].LatestUpdate == nil || got[0].LatestUpdate.Text != "counting now" {
		t.Fatalf("after a fetch and two updates: %+v", got)
	}
	var others []Sent
	decode(t, r.asAgent("helper", "GET", "/v1/messages?sent=1", ""), &others)
	if len(others) != 0 {
		t.Errorf("the recipient sees the sender's request as its own: %+v", others)
	}
	r.asAgent("helper", "POST", "/v1/messages/"+created.ID+"/resolve", `{"text":"230","outcome":"done"}`).Body.Close()
	if got := sent(); len(got) != 0 {
		t.Errorf("a resolved request is still listed: %+v", got)
	}
}

func TestAnAnswerToAQuestionSaysSoAndNamesTheRequest(t *testing.T) {
	r := newRig(t)
	r.project("lead", "helper")
	response := r.asAgent("lead", "POST", "/v1/messages", `{"to":"agent:helper","text":"report a constant"}`)
	var request struct{ ID string }
	decode(t, response, &request)
	r.asAgent("helper", "GET", "/v1/messages/"+request.ID, "").Body.Close()
	var question struct{ ID string }
	decode(t, r.asAgent("helper", "POST", "/v1/messages/"+request.ID+"/ask", `{"text":"which one?"}`), &question)
	r.asAgent("lead", "GET", "/v1/messages/"+question.ID, "").Body.Close()
	r.asAgent("lead", "POST", "/v1/messages/"+question.ID+"/resolve", `{"text":"the first","outcome":"done"}`).Body.Close()
	r.idle("helper")
	claimed := r.claim("helper", time.Minute)
	want := "answers your question on request " + request.ID + ": fetch it."
	if !strings.Contains(claimed.Line, want) || strings.Contains(claimed.Line, "reply to your earlier request") {
		t.Errorf("announced %q, want it to contain %q", claimed.Line, want)
	}
	var got Message
	decode(t, r.asAgent("helper", "GET", "/v1/messages/"+claimed.IDs[0], ""), &got)
	if got.ReKind != kindQuestion || got.On != request.ID || got.Text != "the first" {
		t.Errorf("fetched %+v", got)
	}
}

func TestAKeyMakesASendIdempotentPerSenderAndSurvivesARestart(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	send := func(body string) (int, string) {
		response := r.asOperator("POST", "/v1/messages", body)
		var sent struct{ ID string }
		if response.StatusCode == 200 {
			decode(t, response, &sent)
		} else {
			response.Body.Close()
		}
		return response.StatusCode, sent.ID
	}
	code, first := send(`{"to":"agent:coder","from":"user:7","text":"hi","key":"discord-1"}`)
	if code != 200 {
		t.Fatalf("send: %d", code)
	}
	if _, again := send(`{"to":"agent:coder","from":"user:7","text":"hi","key":"discord-1"}`); again != first {
		t.Errorf("the same key sent a second message: %s and %s", first, again)
	}
	if _, other := send(`{"to":"agent:coder","from":"user:8","text":"hi","key":"discord-1"}`); other == first || other == "" {
		t.Errorf("another sender's key is its own: %q", other)
	}
	if n := len(r.srv.events.selectMessages(func(m Message) bool { return m.Kind == kindRequest })); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
	for _, key := range []string{strings.Repeat("k", maxKeyLength+1), "with space", "tab\t"} {
		body := fmt.Sprintf(`{"to":"agent:coder","from":"user:7","text":"hi","key":%q}`, key)
		if code, _ := send(body); code != 400 {
			t.Errorf("key %q: %d, want 400", key, code)
		}
	}
	reopened, err := newServer(r.srv.dir)
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := reopened.events.keyedMessage("user:7", "discord-1"); !ok || id != first {
		t.Errorf("after a restart the key is %q, %v", id, ok)
	}
}

func TestAUserResolvesOnlyWhatIsAddressedToThemAndTheOperatorAnything(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	response := r.asOperator("POST", "/v1/messages", `{"to":"agent:coder","from":"user:7","text":"deploy"}`)
	var sent struct{ ID string }
	decode(t, response, &sent)
	r.srv.fetch("coder", sent.ID)
	question, err := r.srv.ask("coder", sent.ID, "where?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.srv.resolveForPerson("user:8", question, "prod", "done"); statusOf(t, err) != http.StatusBadRequest {
		t.Errorf("another user answered: %v", err)
	}
	if _, err := r.srv.resolveForPerson("user:7", question, "staging", "done"); err != nil {
		t.Errorf("the user it asked: %v", err)
	}
	other, _ := r.srv.ask("coder", sent.ID, "and then?", nil)
	if _, err := r.srv.resolveForPerson("operator", other, "nothing", "done"); err != nil {
		t.Errorf("the operator answers for anyone: %v", err)
	}
}

func TestReadingEventsStartsAtTheEndForANegativeSeqAndAtTheStartPastTheEnd(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	r.request("coder", "one")
	last := r.srv.events.start(-1)
	if last == 0 {
		t.Fatal("no events")
	}
	if got := r.srv.events.start(last + 10); got != 0 {
		t.Errorf("past the end: %d, want 0 (a log that began again)", got)
	}
	if got := r.srv.events.start(last - 1); got != last-1 {
		t.Errorf("inside the log: %d", got)
	}
}
