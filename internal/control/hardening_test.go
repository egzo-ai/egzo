package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- R-15: an agent cannot fill the control sidecar ------------------------------------------------------

func TestAFloodOfHooksIsBoundedButTheActivityStaysCorrect(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 1000; i++ {
		r.hook("coder", "PreToolUse", `{"tool_name":"Bash"}`)
	}
	r.hook("coder", "Stop", `{}`)
	hooks := 0
	for _, e := range r.srv.events.all() {
		if e.Type == "hook" {
			hooks++
		}
	}
	if hooks > 400 {
		t.Errorf("%d hook events were recorded from one burst", hooks)
	}
	if got := r.srv.events.activity("coder"); got != activityIdle {
		t.Errorf("activity = %q: dropping hook events must not lose the state change", got)
	}
}

func TestAFloodOfStatusLinesIsRefusedPastTheBudget(t *testing.T) {
	r := newRig(t)
	refused := 0
	for i := 0; i < 200; i++ {
		response := r.asAgent("coder", "POST", "/v1/status", `{"text":"x"}`)
		response.Body.Close()
		if response.StatusCode == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused < 100 {
		t.Errorf("only %d of 200 status lines were refused", refused)
	}
	other := r.asAgent("reviewer", "POST", "/v1/status", `{"text":"x"}`)
	other.Body.Close()
	if other.StatusCode != http.StatusNoContent {
		t.Errorf("one agent's flood limited another: %d", other.StatusCode)
	}
}

func TestUpdatesAreRateLimitedLikeMessages(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	id := r.request("coder", "work")
	r.srv.fetch("coder", id)
	var last error
	for i := 0; i < 100 && last == nil; i++ {
		_, last = r.srv.update("coder", id, "still going")
	}
	if statusOf(t, last) != http.StatusTooManyRequests {
		t.Errorf("100 updates in a burst were all accepted: %v", last)
	}
}

func TestTheBudgetRefillsWithTime(t *testing.T) {
	r := newRig(t)
	now := time.Now()
	for i := 0; i < 1000 && r.srv.takeBudget("coder", "status", now); i++ {
	}
	if r.srv.takeBudget("coder", "status", now) {
		t.Fatal("the budget was never used up")
	}
	if !r.srv.takeBudget("coder", "status", now.Add(5*time.Second)) {
		t.Error("the budget did not refill")
	}
}

func appendMany(t *testing.T, s *store, n int, event Event) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := s.append(event); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheEventLogIsCompactedWhenItGrowsTooBig(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	id := r.request("coder", "keep me")
	r.srv.events.maxLogBytes = 20 << 10
	appendMany(t, r.srv.events, 400, Event{Type: "hook", Agent: "coder", Text: "PostToolUse", Data: json.RawMessage(`{"tool_name":"Bash"}`)})
	appendMany(t, r.srv.events, 100, Event{Type: "status", Agent: "coder", Text: strings.Repeat("s", 100)})
	last, _ := r.srv.events.append(Event{Type: "activity", Agent: "coder", Text: activityWorking})
	info, _ := os.Stat(filepath.Join(r.srv.dir, "events.jsonl"))
	if info.Size() > 40<<10 {
		t.Errorf("the log is %d bytes after compaction", info.Size())
	}
	if m, ok := r.srv.events.message(id); !ok || m.Text != "keep me" {
		t.Error("a message was lost in compaction")
	}
	if r.srv.events.activity("coder") != activityWorking {
		t.Error("the activity was lost")
	}
	event, _ := r.srv.events.append(Event{Type: "status", Agent: "coder", Text: "after"})
	if event.Seq <= last.Seq {
		t.Errorf("sequence went from %d to %d: it must only grow", last.Seq, event.Seq)
	}
	reopened, err := newServer(r.srv.dir)
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := reopened.events.message(id); !ok || m.Text != "keep me" || reopened.events.activity("coder") != activityWorking {
		t.Error("the compacted log does not rebuild the same state")
	}
	statuses := reopened.agentStatuses()
	if len(statuses) == 0 || statuses[0].Status != "after" {
		t.Errorf("latest status lost: %+v", statuses)
	}
}

func TestCompactionKeepsAnInterruptThatIsStillPending(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	r.srv.events.maxLogBytes = 10 << 10
	r.asOperator("POST", "/messages", `{"to":"agent:coder","text":"stop","interrupt":true}`).Body.Close()
	appendMany(t, r.srv.events, 300, Event{Type: "hook", Agent: "other", Text: "PostToolUse"})
	if !r.srv.events.interruptPending("coder") {
		t.Error("a pending interrupt was dropped by compaction")
	}
}

// --- R-16: hooks record what happened, not what was in it ------------------------------------------------

func TestAHooksPayloadIsReducedToWhatTheSidecarNeeds(t *testing.T) {
	r := newRig(t)
	secret := "ghp_SECRETSECRETSECRETSECRET"
	r.hook("coder", "PostToolUse", `{"tool_name":"Read","tool_input":{"file_path":"/x/.env"},"tool_response":{"content":"TOKEN=`+secret+`"}}`)
	r.hook("coder", "UserPromptSubmit", `{"prompt":"my password is `+secret+`"}`)
	r.hook("coder", "Notification", `{"message":"Claude needs your permission to use Bash","transcript_path":"/x"}`)
	for _, e := range r.srv.events.all() {
		if strings.Contains(string(e.Data)+e.Text, secret) {
			t.Errorf("event %d (%s) holds what the tool or the user wrote: %s", e.Seq, e.Text, e.Data)
		}
	}
	var seen []string
	for _, e := range r.srv.events.all() {
		if e.Type == "hook" {
			seen = append(seen, string(e.Data))
		}
	}
	joined := strings.Join(seen, " ")
	if !strings.Contains(joined, "Read") || !strings.Contains(joined, "needs your permission") {
		t.Errorf("the tool name and the notification text are what the stream is for: %s", joined)
	}
	if got := r.srv.events.activity("coder"); got != activityBlocked {
		t.Errorf("activity = %q: the full payload must still drive the state", got)
	}
}

func TestSummarizeHookIgnoresUnusableAndHugeInput(t *testing.T) {
	if got := summarizeHook("Stop", []byte("not json")); got != nil {
		t.Errorf("summary = %s", got)
	}
	long := `{"message":"` + strings.Repeat("m", 5000) + `"}`
	if got := summarizeHook("Notification", []byte(long)); len(got) > 700 {
		t.Errorf("summary is %d bytes", len(got))
	}
	if got := summarizeHook("Stop", []byte(`{"tool_name":["not","a","string"]}`)); strings.Contains(string(got), "not") {
		t.Errorf("a non-string field was copied: %s", got)
	}
}

func TestAHookLargerThanTheBodyLimitStillDrivesTheActivity(t *testing.T) {
	r := newRig(t)
	huge := `{"tool_name":"Read","tool_response":"` + strings.Repeat("x", 200<<10) + `"}`
	response := r.asAgent("coder", "POST", "/v1/hooks/PreToolUse", huge)
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if r.srv.events.activity("coder") != activityWorking {
		t.Error("the hook was lost because its payload was large")
	}
}

// --- R-18, R-19: the MCP endpoint and the server around it -----------------------------------------------

func TestTheMCPEndpointRefusesAHugeBody(t *testing.T) {
	r := newRig(t)
	request, _ := http.NewRequest("POST", r.agent.URL+"/mcp", bytes.NewReader(bytes.Repeat([]byte("x"), 3<<20)))
	request.SetBasicAuth("coder", r.token("coder"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err == nil {
		defer response.Body.Close()
		if response.StatusCode < 400 {
			t.Errorf("status = %d", response.StatusCode)
		}
	}
}

func TestToolCallsAreBoundToTheAgentThatMadeThemEvenWithOneSharedServer(t *testing.T) {
	r := newRig(t)
	r.project("coder", "reviewer")
	for _, who := range []string{"coder", "reviewer", "coder"} {
		session, err := r.mcpSession(who, r.token(who))
		if err != nil {
			t.Fatal(err)
		}
		call(t, session, "status", map[string]any{"text": "I am " + who})
		session.Close()
	}
	var got []string
	for _, e := range r.srv.events.all() {
		if e.Type == "status" {
			got = append(got, e.Agent+":"+e.Text)
		}
	}
	want := []string{"coder:I am coder", "reviewer:I am reviewer", "coder:I am coder"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("statuses = %v, want %v", got, want)
	}
}

func TestTheAgentServerHasTimeoutsForSlowClients(t *testing.T) {
	server := agentServer(http.NotFoundHandler())
	if server.ReadHeaderTimeout == 0 || server.ReadTimeout == 0 || server.WriteTimeout == 0 || server.IdleTimeout == 0 {
		t.Errorf("timeouts = header %v read %v write %v idle %v", server.ReadHeaderTimeout, server.ReadTimeout, server.WriteTimeout, server.IdleTimeout)
	}
}

// --- R-19, R-20: the project key -------------------------------------------------------------------------

func TestTheProjectKeyIsReadOnceAndKept(t *testing.T) {
	r := newRig(t)
	first, err := r.srv.projectKey()
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(r.srv.dir, "key"))
	second, err := r.srv.projectKey()
	if err != nil || !bytes.Equal(first, second) {
		t.Errorf("the key was read from disk again (err = %v)", err)
	}
}

func TestAMalformedKeyFileIsAnErrorAndIsNeverReplaced(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "key"), []byte("short"), 0o600)
	srv, _ := newServer(dir)
	os.MkdirAll(filepath.Join(dir, "agents"), 0o755)
	os.WriteFile(filepath.Join(dir, "agents", "coder"), []byte("nonce\n"), 0o644)
	if _, err := srv.projectKey(); err == nil {
		t.Fatal("a malformed key was replaced with a new one: every token would change silently")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "key")); string(data) != "short" {
		t.Error("the malformed key file was overwritten")
	}
	response := httpGetToken(srv, "coder")
	if response != http.StatusInternalServerError {
		t.Errorf("token endpoint status = %d", response)
	}
}

func httpGetToken(srv *server, agent string) int {
	recorder := newRecorder()
	srv.handler().ServeHTTP(recorder, mustRequest("GET", "/tokens/"+agent, nil))
	return recorder.Code
}

func TestTheKeyIsCreatedOnceEvenWhenManyAskAtTheSameTime(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	keys := make([][]byte, 20)
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv, _ := newServer(dir) // twenty sidecars on one volume, as after a restart storm
			keys[i], _ = srv.projectKey()
		}()
	}
	wg.Wait()
	for i := range keys {
		if len(keys[i]) != 32 || !bytes.Equal(keys[i], keys[0]) {
			t.Fatalf("key %d differs: two tokens would disagree", i)
		}
	}
	info, _ := os.Stat(filepath.Join(dir, "key"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("left %s behind", e.Name())
		}
	}
}

// --- R-21: spec snapshots --------------------------------------------------------------------------------

func hashOf(body string) string { return sha256Hex([]byte(body)) }

func TestASnapshotMustMatchItsHashAndIsStoredExactly(t *testing.T) {
	r := newRig(t)
	body := "name: demo\n"
	if response := r.asOperator("PUT", "/specs/"+hashOf(body), body); response.StatusCode != http.StatusNoContent {
		t.Fatalf("put: %d", response.StatusCode)
	}
	if response := r.asOperator("PUT", "/specs/"+hashOf("other"), body); response.StatusCode != http.StatusBadRequest {
		t.Errorf("a body that is not the hash: %d", response.StatusCode)
	}
	got := r.asOperator("GET", "/specs/"+hashOf(body), "")
	var out bytes.Buffer
	out.ReadFrom(got.Body)
	if out.String() != body {
		t.Errorf("stored %q", out.String())
	}
}

func TestAnOversizedSnapshotIsRefusedNotTruncated(t *testing.T) {
	r := newRig(t)
	body := strings.Repeat("x", maxSpecSize+1)
	response := r.asOperator("PUT", "/specs/"+hashOf(body), body)
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", response.StatusCode)
	}
	if response := r.asOperator("GET", "/specs/"+hashOf(body), ""); response.StatusCode != http.StatusNotFound {
		t.Errorf("a truncated snapshot was stored (%d)", response.StatusCode)
	}
}

func TestOldSnapshotsAreRetiredAndNoTemporaryFileIsLeft(t *testing.T) {
	r := newRig(t)
	for i := 0; i < maxSpecs+20; i++ {
		body := fmt.Sprintf("version: %d\n", i)
		r.asOperator("PUT", "/specs/"+hashOf(body), body).Body.Close()
		time.Sleep(2 * time.Millisecond) // distinct modification times
	}
	entries, _ := os.ReadDir(filepath.Join(r.srv.dir, "specs"))
	if len(entries) != maxSpecs {
		t.Errorf("%d snapshots kept, want %d", len(entries), maxSpecs)
	}
	newest := fmt.Sprintf("version: %d\n", maxSpecs+19)
	if response := r.asOperator("GET", "/specs/"+hashOf(newest), ""); response.StatusCode != http.StatusOK {
		t.Error("the newest snapshot was retired")
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yaml") {
			t.Errorf("left %s behind", e.Name())
		}
	}
}

// --- R-22: limits and races in the message model ---------------------------------------------------------

func TestAFetchedMessageNeverGoesBackToAnnounced(t *testing.T) {
	s, _ := openStore(t.TempDir())
	s.append(Event{Type: "message", Actor: "operator", ID: "m1", Text: "x", Data: json.RawMessage(`{"to":"agent:coder","kind":"request"}`)})
	s.append(Event{Type: "fetched", Agent: "coder", ID: "m1"})
	s.append(Event{Type: "announced", Agent: "coder", ID: "m1", Data: json.RawMessage(`{"attempt":2,"deadline_ms":1}`)})
	if m, _ := s.message("m1"); m.State != stateFetched {
		t.Errorf("state = %s: an announcement that lost the race with the fetch undid it", m.State)
	}
}

func TestFetchingWhileAnnouncingNeverBreaksResolve(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	r.idle("coder")
	for i := 0; i < 40; i++ {
		id := r.request("coder", fmt.Sprintf("job %d", i))
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); r.srv.claim("coder", time.Millisecond) }()
		go func() { defer wg.Done(); r.srv.fetch("coder", id) }()
		wg.Wait()
		r.srv.fetch("coder", id)
		if _, err := r.srv.resolve("coder", id, "done", "done"); err != nil {
			t.Fatalf("job %d: %v", i, err)
		}
		r.idle("coder")
	}
}

func TestTheOpenLimitHoldsUnderConcurrentSenders(t *testing.T) {
	r := newRig(t)
	names := make([]string, 60)
	for i := range names {
		names[i] = fmt.Sprintf("a%d", i)
	}
	r.project(append([]string{"target"}, names...)...)
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.srv.sendFromAgent(name, "agent:target", "please", "")
		}()
	}
	wg.Wait()
	open := r.srv.events.selectMessages(func(m Message) bool { return m.To == "agent:target" && m.State != stateResolved })
	if len(open) > maxOpenPerRecipient {
		t.Errorf("%d requests are open for one agent, the limit is %d", len(open), maxOpenPerRecipient)
	}
}

func TestPeopleAreProtectedFromAFloodToo(t *testing.T) {
	r := newRig(t)
	r.project("coder", "b", "c", "d")
	accepted := 0
	for i := 0; i < 100; i++ {
		sender := []string{"coder", "b", "c", "d"}[i%4]
		if _, err := r.srv.sendFromAgent(sender, "operator", "look", ""); err == nil {
			accepted++
		}
	}
	if accepted > maxOpenPerRecipient {
		t.Errorf("%d messages are waiting for the operator, the limit is %d", accepted, maxOpenPerRecipient)
	}
	answered := r.srv.events.selectMessages(func(m Message) bool { return m.To == "operator" && m.State != stateResolved })
	if len(answered) == 0 {
		t.Fatal("no message reached the operator")
	}
	r.srv.resolveForPerson("operator", answered[0].ID, "ok", "done")
	if _, err := r.srv.sendFromAgent("b", "operator", "again", ""); err != nil {
		t.Errorf("a slot freed by an answer was not reusable: %v", err)
	}
}

func TestChoicesAreValidated(t *testing.T) {
	r := newRig(t)
	r.project("coder")
	id := r.request("coder", "ask me")
	r.srv.fetch("coder", id)
	cases := map[string][]string{
		"too many":      make([]string, maxChoices+1),
		"too long":      {strings.Repeat("c", maxChoiceLength+1)},
		"empty":         {""},
		"control chars": {"yes\x1b[2J"},
	}
	for name, choices := range cases {
		for i := range choices {
			if choices[i] == "" && name != "empty" {
				choices[i] = "ok"
			}
		}
		if _, err := r.srv.ask("coder", id, "which?", choices); statusOf(t, err) != http.StatusBadRequest {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := r.srv.ask("coder", id, "which?", []string{"yes", "no", "ask me later"}); err != nil {
		t.Errorf("ordinary choices were refused: %v", err)
	}
}

// --- R-23: sequence numbers and a damaged log ------------------------------------------------------------

func TestSequenceNumbersSurviveACorruptLine(t *testing.T) {
	dir := t.TempDir()
	good := func(seq int, text string) string {
		line, _ := json.Marshal(Event{Seq: seq, Type: "status", Agent: "coder", Text: text})
		return string(line)
	}
	os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(good(1, "a")+"\n{corrupt\n"+good(3, "c")+"\n"), 0o644)
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	event, _ := s.append(Event{Type: "status", Agent: "coder", Text: "d"})
	if event.Seq != 4 {
		t.Errorf("seq = %d, want 4: numbers must keep growing past a skipped line", event.Seq)
	}
	if s.skipped != 1 {
		t.Errorf("skipped = %d, want the damaged line counted", s.skipped)
	}
	seen := map[int]bool{}
	for _, e := range s.all() {
		if seen[e.Seq] {
			t.Errorf("sequence number %d is used twice", e.Seq)
		}
		seen[e.Seq] = true
	}
}

func TestAHalfWrittenLastLineDoesNotCorruptTheNextEvent(t *testing.T) {
	dir := t.TempDir()
	line, _ := json.Marshal(Event{Seq: 1, Type: "status", Agent: "coder", Text: "a"})
	os.WriteFile(filepath.Join(dir, "events.jsonl"), append(line, []byte("\n{\"seq\":2,\"type\":\"sta")...), 0o644)
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.append(Event{Type: "status", Agent: "coder", Text: "after the crash"})
	again, _ := openStore(dir)
	found := false
	for _, e := range again.all() {
		found = found || e.Text == "after the crash"
	}
	if !found {
		t.Error("the event written after a crash was glued to the half line and lost")
	}
}

func TestAVeryLongLineDoesNotStopTheSidecarFromStarting(t *testing.T) {
	dir := t.TempDir()
	line, _ := json.Marshal(Event{Seq: 2, Type: "status", Agent: "coder", Text: "ok"})
	os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(strings.Repeat("x", 12<<20)+"\n"+string(line)+"\n"), 0o644)
	s, err := openStore(dir)
	if err != nil {
		t.Fatalf("a long line made the store refuse to open: %v", err)
	}
	if events := s.all(); len(events) != 1 || events[0].Text != "ok" {
		t.Errorf("events = %+v", events)
	}
}

// --- R-37: an agent stuck working is released ------------------------------------------------------------

func TestTheHolderCanReleaseAnAgentThatIsStuckWorking(t *testing.T) {
	r := newRig(t)
	r.hook("coder", "UserPromptSubmit", `{}`)
	response := r.asAgent("coder", "POST", "/v1/activity", `{"state":"idle","if":"working"}`)
	response.Body.Close()
	if got := r.srv.events.activity("coder"); got != activityIdle {
		t.Errorf("activity = %q", got)
	}
}

func TestAConditionalIdleNeverOverridesABlockedAgent(t *testing.T) {
	r := newRig(t)
	r.hook("coder", "Notification", `{"message":"Claude needs your permission"}`)
	response := r.asAgent("coder", "POST", "/v1/activity", `{"state":"idle","if":"working"}`)
	response.Body.Close()
	if got := r.srv.events.activity("coder"); got != activityBlocked {
		t.Errorf("activity = %q: announcing into a permission dialog would answer it", got)
	}
}

// --- R-46: cost does not grow with the history ------------------------------------------------------------

func TestClaimAndFriendsStayFastWithALongHistory(t *testing.T) {
	r := newRig(t)
	r.project("coder", "other")
	r.srv.events.maxLogBytes = 1 << 40
	for i := 0; i < 60000; i++ {
		r.srv.events.memoryAppend(Event{Type: "hook", Agent: "other", Text: "PostToolUse"})
	}
	for i := 0; i < 6000; i++ {
		id := fmt.Sprintf("m%032d", i)
		r.srv.events.memoryAppend(Event{Type: "message", Actor: "operator", ID: id, Text: "x", Agent: "other", Data: json.RawMessage(`{"to":"agent:other","kind":"request"}`)})
		r.srv.events.memoryAppend(Event{Type: "resolved", Actor: "agent:other", ID: id, Agent: "other", Data: json.RawMessage(`{"outcome":"done"}`)})
	}
	r.idle("coder")
	start := time.Now()
	for i := 0; i < 300; i++ {
		r.srv.claim("coder", time.Minute)
		r.srv.listFor("coder")
		r.srv.overlay("coder")
		r.srv.agentStatuses()
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("300 rounds took %v with a long history: the cost grows with it", elapsed)
	}
}

func TestStreamingFromASequenceDoesNotRescanTheWholeLog(t *testing.T) {
	s, _ := openStore(t.TempDir())
	for i := 0; i < 1000; i++ {
		s.memoryAppend(Event{Type: "status", Agent: "a", Text: "x"})
	}
	events, _ := s.since(990, "")
	if len(events) != 10 || events[0].Seq != 991 {
		t.Errorf("since(990) = %d events starting at %d", len(events), events[0].Seq)
	}
	filtered, _ := s.since(0, "nobody")
	if len(filtered) != 0 {
		t.Errorf("filter by agent returned %d events", len(filtered))
	}
}

func TestStartingNeverUndoesWhatTheHarnessAlreadyReported(t *testing.T) {
	r := newRig(t)
	r.idle("coder") // the harness's SessionStart arrived first
	r.asAgent("coder", "POST", "/v1/activity", `{"state":"starting","if_unset":true}`).Body.Close()
	if got := r.srv.events.activity("coder"); got != activityIdle {
		t.Errorf("activity = %q", got)
	}
	r.asAgent("reviewer", "POST", "/v1/activity", `{"state":"starting","if_unset":true}`).Body.Close()
	if got := r.srv.events.activity("reviewer"); got != activityStarting {
		t.Errorf("a first report was ignored: %q", got)
	}
}

func TestTheLineEgzoTypedIsRecordedButWhatAPersonTypedIsNot(t *testing.T) {
	line := "check egzo message m0123456789abcdef0123456789abcdef and handle the request for me."
	got := string(summarizeHook("UserPromptSubmit", []byte(`{"prompt":"`+line+`"}`)))
	if !strings.Contains(got, "check egzo message m0123456789abcdef") {
		t.Errorf("the announcement is how the specs and the operator see what was typed: %s", got)
	}
	if got := summarizeHook("UserPromptSubmit", []byte(`{"prompt":"egzo message with my password hunter2"}`)); got != nil {
		t.Errorf("a person's text that merely starts alike was recorded: %s", got)
	}
}
