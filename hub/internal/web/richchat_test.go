package web_test

import (
	"bufio"
	"net"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/egzo-ai/egzo/hub/internal/rbac"
)

func TestTheRichChatBridgesTheBrowserToTheAgentsAHP(t *testing.T) {
	r := newRig(t)
	r.link("web", "oc")
	rich := agent("oc-1", "operator")
	rich.AHP = true
	r.fakes["web"].SetAgent(rich)
	r.fakes["web"].SetAgent(agent("plain-1", "operator"))
	heard := make(chan bool, 4)
	r.fakes["web"].ServeAHP("oc-1", func(conn net.Conn, readOnly bool) {
		defer conn.Close()
		heard <- readOnly
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			conn.Write([]byte(strings.Replace(scanner.Text(), `"ping"`, `"pong"`, 1) + "\n"))
		}
	})
	root := r.browser()
	root.signIn("admin")
	path := "/p/" + r.id("web") + "/a/oc-1/chat"
	chat, err := root.dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer chat.CloseNow()
	if m := readControl(t, chat); m["method"] != "x-egzo/chatOpened" || at(m, "params", "readOnly") != false {
		t.Fatalf("first frame: %v", m)
	}
	chat.Write(ctx, websocket.MessageText, []byte("{\"jsonrpc\":\"2.0\",\n\"id\":1,\"method\":\"ping\"}"))
	if m := readControl(t, chat); m["method"] != "pong" {
		t.Errorf("the line came back: %v", m)
	}
	if ro := <-heard; ro {
		t.Error("an administrator acts in the chat")
	}

	// a user who may not operate the agent watches
	bob := r.browser()
	bob.signIn("bob")
	r.store.SetRole(ctx, r.id("web"), r.userID("bob"), rbac.User)
	watching, err := bob.dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer watching.CloseNow()
	if m := readControl(t, watching); at(m, "params", "readOnly") != true {
		t.Errorf("bob watches: %v", m)
	}
	if ro := <-heard; !ro {
		t.Error("the link was not told bob only watches")
	}

	// an agent spawned without it has no chat, and someone without a role gets nothing
	if _, err := root.dial("/p/" + r.id("web") + "/a/plain-1/chat"); err == nil {
		t.Error("an agent without the Rich UI opened a chat")
	}
	eve := r.browser()
	eve.signIn("eve")
	if _, err := eve.dial(path); err == nil {
		t.Error("a person without a role opened the chat")
	}
}
