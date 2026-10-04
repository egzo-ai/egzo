package control

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func (r *rig) hook(agent, name, payload string) {
	r.t.Helper()
	response := r.asAgent(agent, "POST", "/v1/hooks/"+name, payload)
	response.Body.Close()
	if response.StatusCode != 204 {
		r.t.Fatalf("hook %s: status %d", name, response.StatusCode)
	}
}

func (r *rig) claim(agent string, timeout time.Duration) claimResult {
	r.t.Helper()
	response := r.asAgent(agent, "POST", "/v1/claim", `{"ack_timeout_ms":`+itoa(timeout.Milliseconds())+`}`)
	defer response.Body.Close()
	var result claimResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		r.t.Fatal(err)
	}
	return result
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func (r *rig) enqueue(to, text string) string {
	r.t.Helper()
	response := r.asOperator("POST", "/queue", `{"to":"`+to+`","text":"`+text+`"}`)
	defer response.Body.Close()
	var queued struct{ ID string }
	json.NewDecoder(response.Body).Decode(&queued)
	return queued.ID
}

func (r *rig) states(agent string) map[string]string {
	out := map[string]string{}
	for _, message := range r.srv.events.messages(agent) {
		out[message.ID] = message.State
	}
	return out
}

func TestHooksDriveTheActivityOfAnAgent(t *testing.T) {
	r := newRig(t)
	steps := []struct{ hook, payload, want string }{
		{"SessionStart", `{}`, "idle"},
		{"UserPromptSubmit", `{"prompt":"go"}`, "busy"},
		{"Notification", `{"message":"Claude needs your permission to use Bash"}`, "blocked"},
		{"PostToolUse", `{}`, "busy"},
		{"Stop", `{}`, "idle"},
		{"Notification", `{"message":"Claude is waiting for your input"}`, "idle"},
	}
	for _, step := range steps {
		r.hook("coder", step.hook, step.payload)
		if got := r.srv.events.activity("coder"); got != step.want {
			t.Fatalf("after %s: activity %q, want %q", step.hook, got, step.want)
		}
	}
	if r.srv.events.activity("reviewer") != "" {
		t.Error("an agent's hooks changed another agent's activity")
	}
}

func TestAnUnchangedActivityIsNotAnEvent(t *testing.T) {
	r := newRig(t)
	r.hook("coder", "Stop", `{}`)
	r.hook("coder", "Stop", `{}`)
	count := 0
	for _, event := range r.srv.events.all() {
		if event.Type == "activity" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("%d activity events, want 1", count)
	}
}

func TestOnlyAnIdleAgentIsHandedItsMessages(t *testing.T) {
	r := newRig(t)
	r.enqueue("coder", "first")
	if got := r.claim("coder", time.Minute); len(got.Messages) != 0 {
		t.Fatalf("an agent that has not reported idle was handed %v", got.Messages)
	}
	r.hook("coder", "SessionStart", `{}`)
	r.hook("coder", "UserPromptSubmit", `{"prompt":"busy now"}`)
	if got := r.claim("coder", time.Minute); len(got.Messages) != 0 {
		t.Fatalf("a busy agent was handed %v", got.Messages)
	}
	r.hook("coder", "Stop", `{}`)
	got := r.claim("coder", time.Minute)
	if len(got.Messages) != 1 || got.Messages[0].Text != "first" || got.Messages[0].From != "operator" {
		t.Fatalf("claim = %+v", got)
	}
}

func TestQueuedMessagesAreHandedOverTogetherAndNotTwice(t *testing.T) {
	r := newRig(t)
	r.hook("coder", "SessionStart", `{}`)
	a, b := r.enqueue("coder", "alpha"), r.enqueue("coder", "beta")
	got := r.claim("coder", time.Minute)
	if len(got.Messages) != 2 || got.Messages[0].ID != a || got.Messages[1].ID != b {
		t.Fatalf("claim = %+v", got)
	}
	if again := r.claim("coder", time.Minute); len(again.Messages) != 0 {
		t.Errorf("the same messages were handed over twice: %+v", again)
	}
	c := r.enqueue("coder", "gamma")
	if more := r.claim("coder", time.Minute); len(more.Messages) != 0 {
		t.Errorf("a message was handed over while the earlier ones await their acknowledgement: %+v (%s)", more, c)
	}
}

func TestAPromptHookAcknowledgesTheMessagesItCarries(t *testing.T) {
	r := newRig(t)
	r.hook("coder", "SessionStart", `{}`)
	a, b := r.enqueue("coder", "alpha"), r.enqueue("coder", "beta")
	r.claim("coder", time.Minute)
	prompt, _ := json.Marshal(map[string]string{"prompt": "[egzo msg " + a + " from operator] alpha"})
	r.hook("coder", "UserPromptSubmit", string(prompt))
	states := r.states("coder")
	if states[a] != messageDelivered || states[b] != messageDelivering {
		t.Errorf("states = %v", states)
	}
	other, _ := json.Marshal(map[string]string{"prompt": "unrelated text mentioning [egzo msg m0000 from x]"})
	r.hook("coder", "UserPromptSubmit", string(other))
	if r.states("coder")[b] != messageDelivering {
		t.Error("a prompt without the message's id acknowledged it")
	}
}

func TestAnAgentCannotAcknowledgeAnotherAgentsMessage(t *testing.T) {
	r := newRig(t)
	r.hook("coder", "SessionStart", `{}`)
	a := r.enqueue("coder", "private")
	r.claim("coder", time.Minute)
	prompt, _ := json.Marshal(map[string]string{"prompt": "[egzo msg " + a + " from operator]"})
	r.hook("reviewer", "UserPromptSubmit", string(prompt))
	if r.states("coder")[a] != messageDelivering {
		t.Error("a hook of another agent acknowledged the message")
	}
}

func TestAMessageNobodyAcknowledgesBecomesUnconfirmedAndIsNeverHandedOverAgain(t *testing.T) {
	r := newRig(t)
	r.hook("coder", "SessionStart", `{}`)
	a := r.enqueue("coder", "hello?")
	r.claim("coder", 50*time.Millisecond)
	r.srv.expire(time.Now())
	if r.states("coder")[a] != messageDelivering {
		t.Fatal("expired before its deadline")
	}
	r.srv.expire(time.Now().Add(time.Second))
	if r.states("coder")[a] != messageUnconfirmed {
		t.Fatalf("state = %s, want unconfirmed", r.states("coder")[a])
	}
	if again := r.claim("coder", time.Minute); len(again.Messages) != 0 {
		t.Errorf("an unconfirmed message was retried: %+v", again)
	}
}

func TestAnInterruptIsHandedOverOnceAndMarksTheAgentIdle(t *testing.T) {
	r := newRig(t)
	r.hook("coder", "UserPromptSubmit", `{"prompt":"long job"}`)
	response := r.asOperator("POST", "/queue", `{"to":"coder","text":"change of plan","interrupt":true}`)
	response.Body.Close()
	got := r.claim("coder", time.Minute)
	if !got.Interrupt {
		t.Fatalf("claim = %+v, want an interrupt", got)
	}
	if r.srv.events.activity("coder") != "idle" {
		t.Error("control did not mark the interrupted agent idle")
	}
	next := r.claim("coder", time.Minute)
	if next.Interrupt || len(next.Messages) != 1 {
		t.Errorf("after the interrupt: %+v", next)
	}
}

func TestTheHolderCanReportActivityAndAcknowledgeByIDs(t *testing.T) {
	r := newRig(t)
	response := r.asAgent("coder", "POST", "/v1/activity", `{"state":"idle"}`)
	response.Body.Close()
	bad := r.asAgent("coder", "POST", "/v1/activity", `{"state":"dancing"}`)
	bad.Body.Close()
	if r.srv.events.activity("coder") != "idle" || bad.StatusCode != 400 {
		t.Fatalf("activity %q, bad status %d", r.srv.events.activity("coder"), bad.StatusCode)
	}
	a := r.enqueue("coder", "echoed")
	r.claim("coder", time.Minute)
	ack := r.asAgent("coder", "POST", "/v1/ack", `{"ids":["`+a+`"]}`)
	ack.Body.Close()
	if r.states("coder")[a] != messageDelivered {
		t.Errorf("state = %s", r.states("coder")[a])
	}
}

func TestTheQueueListsWhatIsNotFinishedWithItsState(t *testing.T) {
	r := newRig(t)
	r.hook("coder", "SessionStart", `{}`)
	r.enqueue("coder", "one")
	r.claim("coder", time.Minute)
	r.enqueue("coder", "two")
	response := r.asOperator("GET", "/queue?agent=coder", "")
	defer response.Body.Close()
	var messages []Message
	json.NewDecoder(response.Body).Decode(&messages)
	var states []string
	for _, m := range messages {
		states = append(states, m.State)
	}
	if strings.Join(states, ",") != "delivering,queued" {
		t.Errorf("states = %v", states)
	}
}

func TestAgentsListsTheActivityNextToTheStatus(t *testing.T) {
	r := newRig(t)
	r.asAgent("coder", "POST", "/v1/status", `{"text":"parsing"}`).Body.Close()
	r.hook("coder", "UserPromptSubmit", `{"prompt":"x"}`)
	response := r.asOperator("GET", "/agents", "")
	defer response.Body.Close()
	var agents []AgentStatus
	json.NewDecoder(response.Body).Decode(&agents)
	if len(agents) != 1 || agents[0].Status != "parsing" || agents[0].Activity != "busy" {
		t.Errorf("agents = %+v", agents)
	}
}

func TestHandoffQueuesAMessageFromTheCallingAgentForAnotherOne(t *testing.T) {
	r := newRig(t)
	project := r.asOperator("PUT", "/project", `{"agents":["coder","reviewer"]}`)
	project.Body.Close()
	if err := r.srv.handoff("coder", "reviewer", "please review branch x"); err != nil {
		t.Fatal(err)
	}
	messages := r.srv.events.pending("reviewer")
	if len(messages) != 1 || messages[0].From != "agent:coder" || messages[0].Text != "please review branch x" {
		t.Errorf("messages = %+v", messages)
	}
}

func TestHandoffRefusesUnknownAgentsOneselfAndEmptyText(t *testing.T) {
	r := newRig(t)
	r.asOperator("PUT", "/project", `{"agents":["coder","reviewer"]}`).Body.Close()
	for name, call := range map[string]func() error{
		"unknown": func() error { return r.srv.handoff("coder", "ghost", "x") },
		"self":    func() error { return r.srv.handoff("coder", "coder", "x") },
		"empty":   func() error { return r.srv.handoff("coder", "reviewer", "  ") },
	} {
		if call() == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if len(r.srv.events.all()) != 0 {
		t.Error("a refused handoff left an event behind")
	}
}

func TestBeforeTheProjectSaysWhichAgentsExistAnyWellFormedNameIsAccepted(t *testing.T) {
	r := newRig(t)
	if err := r.srv.handoff("coder", "reviewer", "hi"); err != nil {
		t.Errorf("err = %v", err)
	}
	if err := r.srv.handoff("coder", "../etc", "hi"); err == nil {
		t.Error("a malformed name was accepted")
	}
}
