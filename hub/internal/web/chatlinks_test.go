// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package web_test

import "testing"

func TestAPersonConfirmsAChatLinkSignedInSeesTheAccountAndRemovesIt(t *testing.T) {
	r := newRig(t)
	code, _, err := r.hub.BridgeCode(ctx, "discord", "d-7", "joe")
	if err != nil {
		t.Fatal(err)
	}
	anonymous := r.browser()
	if status, _ := anonymous.post("/chat-links/peek", json_{"code": code}); status != 401 {
		t.Errorf("peeking signed out = %d", status)
	}
	alice := r.browser()
	alice.signIn("alice")
	if status, out := alice.post("/chat-links/peek", json_{"code": code}); status != 200 || out["bridge"] != "discord" || out["name"] != "joe" {
		t.Errorf("peek = %d %v", status, out)
	}
	if status, out := alice.post("/chat-links/peek", json_{"code": "egzo-chat-wrong"}); status != 404 || out["code"] != "bad_code" {
		t.Errorf("a wrong code = %d %v", status, out)
	}
	if status, out := alice.post("/chat-links/confirm", json_{"code": code}); status != 200 || out["account"] != "d-7" {
		t.Errorf("confirm = %d %v", status, out)
	}
	if status, _ := alice.post("/chat-links/confirm", json_{"code": code}); status != 404 {
		t.Errorf("a code twice = %d", status)
	}
	status, out := alice.get("/me/chat-accounts")
	if list, _ := out["accounts"].([]any); status != 200 || len(list) != 1 {
		t.Errorf("accounts = %d %v", status, out)
	}
	if status, _ := alice.del("/me/chat-accounts/discord/d-7"); status != 200 {
		t.Errorf("unlink = %d", status)
	}
	if status, _ := alice.del("/me/chat-accounts/discord/d-7"); status != 404 {
		t.Errorf("unlink twice = %d", status)
	}
}

func TestTheOperatorSeesWhichChatAccountsEachUserLinked(t *testing.T) {
	r := newRig(t)
	code, _, err := r.hub.BridgeCode(ctx, "discord", "d-7", "joe")
	if err != nil {
		t.Fatal(err)
	}
	alice := r.browser()
	alice.signIn("alice")
	if status, out := alice.post("/chat-links/confirm", json_{"code": code}); status != 200 {
		t.Fatalf("confirm = %d %v", status, out)
	}
	op := r.browser()
	op.signIn("admin")
	_, users := op.get("/users")
	for _, u := range list(users, "users") {
		chats := list(u, "chatAccounts")
		switch u["username"] {
		case "alice":
			if len(chats) != 1 || chats[0]["name"] != "joe" || chats[0]["bridge"] != "discord" {
				t.Errorf("alice's chat accounts = %v", u["chatAccounts"])
			}
		default:
			if _, ok := u["chatAccounts"].([]any); !ok || len(chats) != 0 {
				t.Errorf("%v's chat accounts = %v", u["username"], u["chatAccounts"])
			}
		}
	}
}
