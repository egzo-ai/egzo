package secretbox

import (
	"bytes"
	"strings"
	"testing"
)

func TestSealAndOpen(t *testing.T) {
	box, err := New("0123456789abcdef-secret")
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := box.Seal("engine:a", []byte("tls key"))
	if bytes.Contains(sealed, []byte("tls key")) {
		t.Fatal("the value is in the clear")
	}
	plain, err := box.Open("engine:a", sealed)
	if err != nil || string(plain) != "tls key" {
		t.Fatalf("open = %q, %v", plain, err)
	}
	again, _ := box.Seal("engine:a", []byte("tls key"))
	if bytes.Equal(again, sealed) {
		t.Error("two seals of one value are identical: the nonce is reused")
	}
}

func TestAValueCannotBeMovedOrOpenedWithAnotherKey(t *testing.T) {
	box, _ := New("0123456789abcdef-secret")
	other, _ := New("another-secret-key-here")
	sealed, _ := box.Seal("engine:a", []byte("x"))
	if _, err := box.Open("engine:b", sealed); err == nil {
		t.Error("opened under another label")
	}
	if _, err := other.Open("engine:a", sealed); err == nil {
		t.Error("opened with another key")
	}
	if _, err := box.Open("engine:a", []byte("short")); err == nil {
		t.Error("opened a truncated value")
	}
}

func TestAShortKeyIsRefused(t *testing.T) {
	if _, err := New("short"); err == nil || !strings.Contains(err.Error(), "EGZO_SECRET_KEY") {
		t.Errorf("err = %v", err)
	}
}
