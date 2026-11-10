package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestABridgeCodeLinksAnAccountOnceAndTheAccountFollowsWhoeverConfirmsLast(t *testing.T) {
	s := open(t)
	alice, bob := login(t, s, "alice"), login(t, s, "bob")
	now := time.Now()
	code, expires, err := s.CreateBridgeCode(ctx, "discord", "d-7", "joe#1", now)
	if err != nil || !strings.HasPrefix(code, BridgeCodePrefix) || !expires.After(now.Add(14*time.Minute)) {
		t.Fatalf("code %q %v %v", code, expires, err)
	}
	if c, err := s.BridgeCodeFor(ctx, code, now); err != nil || c.ExternalName != "joe#1" || c.Bridge != "discord" {
		t.Errorf("what it is for: %+v %v", c, err)
	}
	if _, err := s.BridgeCodeFor(ctx, code, now.Add(BridgeCodeLife+time.Second)); !errors.Is(err, ErrBadCode) {
		t.Errorf("an expired code: %v", err)
	}
	a, err := s.ConfirmBridgeCode(ctx, code, alice.ID, now)
	if err != nil || a.UserID != alice.ID {
		t.Fatalf("confirm: %+v %v", a, err)
	}
	if _, err := s.ConfirmBridgeCode(ctx, code, bob.ID, now); !errors.Is(err, ErrBadCode) {
		t.Errorf("a code twice: %v", err)
	}
	if got, err := s.BridgeAccountOf(ctx, "discord", "d-7"); err != nil || got.UserID != alice.ID || got.Username != "alice" {
		t.Errorf("the account: %+v %v", got, err)
	}
	again, _, _ := s.CreateBridgeCode(ctx, "discord", "d-7", "joe#1", now)
	if _, err := s.ConfirmBridgeCode(ctx, again, bob.ID, now); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.BridgeAccountOf(ctx, "discord", "d-7"); got.UserID != bob.ID {
		t.Errorf("the account is the last confirmer's: %+v", got)
	}
	if list, _ := s.BridgeAccountsOf(ctx, alice.ID); len(list) != 0 {
		t.Errorf("alice still has %+v", list)
	}
	if ok, _ := s.UnlinkBridgeAccount(ctx, alice.ID, "discord", "d-7"); ok {
		t.Error("alice unlinked bob's account")
	}
	if ok, _ := s.UnlinkBridgeAccount(ctx, bob.ID, "discord", "d-7"); !ok {
		t.Error("bob could not unlink his")
	}
	if _, err := s.BridgeAccountOf(ctx, "discord", "d-7"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after unlinking: %v", err)
	}
	if list, _ := s.BridgeAccounts(ctx, "discord"); len(list) != 0 {
		t.Errorf("accounts left %+v", list)
	}
}

func TestExpiredBridgeCodesArePurged(t *testing.T) {
	s := open(t)
	now := time.Now()
	s.CreateBridgeCode(ctx, "discord", "d-7", "joe", now.Add(-time.Hour))
	live, _, _ := s.CreateBridgeCode(ctx, "discord", "d-8", "ann", now)
	if err := s.PurgeBridgeCodes(ctx, now); err != nil {
		t.Fatal(err)
	}
	var kept int
	s.db.QueryRow(`SELECT COUNT(*) FROM bridge_codes`).Scan(&kept)
	if kept != 1 {
		t.Errorf("%d codes are kept", kept)
	}
	if c, err := s.BridgeCodeFor(ctx, live, now); err != nil || c.ExternalID != "d-8" {
		t.Errorf("the live code: %+v, %v", c, err)
	}
}
