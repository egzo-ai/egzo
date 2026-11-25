// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package ahp

import (
	"encoding/json"
	"strings"
	"testing"
)

func mainKnown(f *Filter) {
	f.FromServer([]byte(`{"jsonrpc":"2.0","method":"x-egzo/mainSession","params":{"session":"ahp-session:/ses_main","chat":"ahp-chat:/ses_main"}}`))
}

func TestClientMayOnlyReachTheMainSessionsChat(t *testing.T) {
	f := &Filter{}
	if fwd, _ := f.FromClient([]byte(`{"jsonrpc":"2.0","id":1,"method":"subscribe","params":{"channel":"ahp-chat:/ses_main"}}`)); fwd != nil {
		t.Fatal("before the main session is known, no session can be subscribed to")
	}
	mainKnown(f)
	allowed := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"channel":"ahp-root://","clientId":"hub","protocolVersions":["1.0.0"],"initialSubscriptions":["ahp-root://"]}}`,
		`{"jsonrpc":"2.0","id":2,"method":"subscribe","params":{"channel":"ahp-chat:/ses_main"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"subscribe","params":{"channel":"ahp-session:/ses_main"}}`,
		`{"jsonrpc":"2.0","method":"dispatchAction","params":{"channel":"ahp-chat:/ses_main","clientSeq":1,"action":{"type":"chat/turnStarted","turnId":"t","message":{"text":"hi"}}}}`,
		`{"jsonrpc":"2.0","method":"dispatchAction","params":{"channel":"ahp-chat:/ses_main","clientSeq":2,"action":{"type":"chat/turnCancelled","turnId":"t"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"listSessions","params":{}}`,
		`{"jsonrpc":"2.0","id":5,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":6,"method":"fetchTurns","params":{"channel":"ahp-chat:/ses_main"}}`,
	}
	for _, line := range allowed {
		if fwd, reply := f.FromClient([]byte(line)); fwd == nil {
			t.Errorf("refused %s: %s", line, reply)
		}
	}
	refused := []string{
		`{"jsonrpc":"2.0","id":10,"method":"resourceRead","params":{"uri":"file:///etc/passwd"}}`,
		`{"jsonrpc":"2.0","id":11,"method":"resourceList","params":{"uri":"file:///"}}`,
		`{"jsonrpc":"2.0","id":12,"method":"createTerminal","params":{"channel":"x-term://1"}}`,
		`{"jsonrpc":"2.0","id":13,"method":"createSession","params":{"channel":"ahp-session:/new"}}`,
		`{"jsonrpc":"2.0","id":14,"method":"disposeSession","params":{"channel":"ahp-session:/ses_main"}}`,
		`{"jsonrpc":"2.0","id":15,"method":"createChat","params":{"channel":"ahp-session:/ses_main"}}`,
		`{"jsonrpc":"2.0","id":16,"method":"invokeChangesetOperation","params":{}}`,
		`{"jsonrpc":"2.0","id":17,"method":"subscribe","params":{"channel":"ahp-chat:/ses_other"}}`,
		`{"jsonrpc":"2.0","id":18,"method":"initialize","params":{"channel":"ahp-root://","initialSubscriptions":["ahp-chat:/ses_other"]}}`,
		`{"jsonrpc":"2.0","method":"dispatchAction","params":{"channel":"ahp-chat:/ses_other","clientSeq":3,"action":{"type":"chat/turnStarted"}}}`,
		`{"jsonrpc":"2.0","method":"dispatchAction","params":{"channel":"ahp-chat:/ses_main","clientSeq":4,"action":{"type":"chat/truncated"}}}`,
		`{"jsonrpc":"2.0","method":"dispatchAction","params":{"channel":"ahp-session:/ses_main","clientSeq":5,"action":{"type":"session/titleChanged","title":"x"}}}`,
		`{"jsonrpc":"2.0","id":19,"method":"fetchTurns","params":{"channel":"ahp-chat:/ses_other"}}`,
		`not json`,
	}
	for _, line := range refused {
		fwd, reply := f.FromClient([]byte(line))
		if fwd != nil {
			t.Errorf("let through %s", line)
		}
		if strings.Contains(line, `"id"`) && reply == nil {
			t.Errorf("no error answer to %s", line)
		}
		if reply != nil {
			var answer struct {
				Error struct{ Code int } `json:"error"`
			}
			if json.Unmarshal(reply, &answer) != nil || answer.Error.Code != errNotAllowed {
				t.Errorf("answer to %s: %s", line, reply)
			}
		}
	}
}

func TestReadOnlyClientWatches(t *testing.T) {
	f := &Filter{ReadOnly: true}
	mainKnown(f)
	if fwd, _ := f.FromClient([]byte(`{"jsonrpc":"2.0","id":2,"method":"subscribe","params":{"channel":"ahp-chat:/ses_main"}}`)); fwd == nil {
		t.Fatal("a read-only client subscribes")
	}
	if fwd, _ := f.FromClient([]byte(`{"jsonrpc":"2.0","method":"dispatchAction","params":{"channel":"ahp-chat:/ses_main","clientSeq":1,"action":{"type":"chat/turnStarted"}}}`)); fwd != nil {
		t.Fatal("a read-only client sends no prompt")
	}
}

func TestServerSaysOnlyWhatIsAboutTheMainSession(t *testing.T) {
	f := &Filter{}
	mainKnown(f)
	pass := []string{
		`{"jsonrpc":"2.0","method":"action","params":{"channel":"ahp-chat:/ses_main","action":{"type":"chat/responsePart"},"serverSeq":3}}`,
		`{"jsonrpc":"2.0","method":"action","params":{"channel":"ahp-root://","action":{"type":"root/activeSessionsChanged","activeSessions":2},"serverSeq":4}}`,
		`{"jsonrpc":"2.0","method":"root/sessionAdded","params":{"channel":"ahp-root://","summary":{"resource":"ahp-session:/ses_main"}}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"1.0.0"}}`,
	}
	for _, line := range pass {
		if f.FromServer([]byte(line)) == nil {
			t.Errorf("dropped %s", line)
		}
	}
	drop := []string{
		`{"jsonrpc":"2.0","method":"action","params":{"channel":"ahp-chat:/ses_other","action":{"type":"chat/responsePart"},"serverSeq":5}}`,
		`{"jsonrpc":"2.0","method":"root/sessionAdded","params":{"channel":"ahp-root://","summary":{"resource":"ahp-session:/ses_other","title":"secret"}}}`,
		`{"jsonrpc":"2.0","method":"root/sessionSummaryChanged","params":{"channel":"ahp-root://","session":"ahp-session:/ses_other","changes":{"title":"secret"}}}`,
		`garbage`,
	}
	for _, line := range drop {
		if f.FromServer([]byte(line)) != nil {
			t.Errorf("let through %s", line)
		}
	}
}

func TestListSessionsIsCutToTheMainSession(t *testing.T) {
	f := &Filter{}
	mainKnown(f)
	f.FromClient([]byte(`{"jsonrpc":"2.0","id":7,"method":"listSessions","params":{}}`))
	out := f.FromServer([]byte(`{"jsonrpc":"2.0","id":7,"result":{"items":[{"resource":"ahp-session:/ses_main","title":"a"},{"resource":"ahp-session:/ses_other","title":"secret"}],"nextCursor":"2"}}`))
	if strings.Contains(string(out), "secret") || !strings.Contains(string(out), "ses_main") || strings.Contains(string(out), "nextCursor") {
		t.Fatalf("listSessions answer: %s", out)
	}
	// another answer with the same id later is not cut again (and passes as an answer)
	if f.FromServer([]byte(`{"jsonrpc":"2.0","id":8,"result":{"items":[{"resource":"ahp-session:/ses_other"}]}}`)) == nil {
		t.Fatal("an answer to something else passes")
	}
}

func TestMainSessionNoteMustBeWellFormed(t *testing.T) {
	f := &Filter{}
	if f.FromServer([]byte(`{"jsonrpc":"2.0","method":"x-egzo/mainSession","params":{"session":""}}`)) != nil {
		t.Fatal("a note with no session is dropped")
	}
	if f.Main().Session != "" {
		t.Fatal("and changes nothing")
	}
}

func TestSessionURIs(t *testing.T) {
	got := SessionURIs("ses_x")
	if got.Session != "ahp-session:/ses_x" || got.Chat != "ahp-chat:/ses_x" {
		t.Fatal(got)
	}
	if validSessionID("../etc") || !validSessionID("ses_df13c6555ffeMErBfD1QPgTcmv") {
		t.Fatal("ids")
	}
}

// The plugin reads its messages with JSON.parse, where names are exact and the last of two equal names wins. Go's decoder into a struct
// matches names whatever their case: the filter must read a message as the plugin will, or a refused method passes under an allowed one.
func TestTheFilterReadsAMessageAsThePluginDoes(t *testing.T) {
	f := &Filter{}
	mainKnown(f)
	for _, line := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"resourceRead","params":{"uri":"file:///etc/passwd"},"Method":"ping"}`,
		`{"jsonrpc":"2.0","id":2,"METHOD":"ping","method":"createTerminal","params":{"channel":"x-term://1"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"subscribe","params":{"channel":"ahp-chat:/ses_other"},"Params":{"channel":"ahp-chat:/ses_main"}}`,
		`{"jsonrpc":"2.0","method":"dispatchAction","params":{"channel":"ahp-chat:/ses_main","clientSeq":1,"action":{"type":"chat/truncated","Type":"chat/turnStarted"}}}`,
		`{"jsonrpc":"2.0","method":"dispatchAction","params":{"channel":"ahp-chat:/ses_main","clientSeq":1,"action":{"TYPE":"chat/turnStarted","type":"chat/truncated"}}}`,
	} {
		if fwd, _ := f.FromClient([]byte(line)); fwd != nil {
			t.Errorf("let through %s", line)
		}
	}
	// two equal names: the last one counts, for the plugin and for the filter
	if fwd, _ := f.FromClient([]byte(`{"jsonrpc":"2.0","id":4,"method":"ping","method":"resourceRead","params":{"uri":"file:///etc/passwd"}}`)); fwd != nil {
		t.Error("let through a resourceRead behind an earlier ping")
	}
	if fwd, _ := f.FromClient([]byte(`{"jsonrpc":"2.0","id":5,"method":"resourceRead","method":"ping"}`)); fwd == nil {
		t.Error("refused a ping, the last of two methods")
	}
}

// The answer to a listSessions is cut down to the main session. It is known by its id, as the plugin echoes it: an id that comes back
// written another way (1.0 is 1 to JSON.parse), or one that two requests share, must not let the whole list through.
func TestTheAnswerToAListSessionsCannotSlipPastItsCut(t *testing.T) {
	list := `{"jsonrpc":"2.0","id":%s,"result":{"items":[{"resource":"ahp-session:/ses_main"},{"resource":"ahp-session:/ses_other","title":"secret"}]}}`
	f := &Filter{}
	mainKnown(f)
	if fwd, _ := f.FromClient([]byte(`{"jsonrpc":"2.0","id":1.0,"method":"listSessions","params":{}}`)); fwd != nil {
		if out := f.FromServer([]byte(strings.Replace(list, "%s", "1", 1))); strings.Contains(string(out), "ses_other") {
			t.Errorf("an id written 1.0 let the whole list through: %s", out)
		}
	}
	f = &Filter{}
	mainKnown(f)
	if fwd, reply := f.FromClient([]byte(`{"jsonrpc":"2.0","id":7,"method":"listSessions","params":{}}`)); fwd == nil {
		t.Fatalf("refused a listSessions: %s", reply)
	}
	if fwd, _ := f.FromClient([]byte(`{"jsonrpc":"2.0","id":7,"method":"ping"}`)); fwd != nil {
		// the ping's answer would take the cut, and the list's would pass whole
		f.FromServer([]byte(`{"jsonrpc":"2.0","id":7,"result":{}}`))
		if out := f.FromServer([]byte(strings.Replace(list, "%s", "7", 1))); strings.Contains(string(out), "ses_other") {
			t.Errorf("a request that reused the list's id let the whole list through: %s", out)
		}
	}
	f = &Filter{}
	mainKnown(f)
	f.FromClient([]byte(`{"jsonrpc":"2.0","id":"ab","method":"listSessions","params":{}}`))
	if out := f.FromServer([]byte(strings.Replace(list, "%s", `"ab"`, 1))); strings.Contains(string(out), "ses_other") {
		t.Errorf("a string id written with an escape let the whole list through: %s", out)
	}
}
