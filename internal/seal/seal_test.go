// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package seal

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

var secret = []byte(strings.Repeat("k", MinKeyBytes))

func keys(t *testing.T, secret []byte, project string) *Keys {
	t.Helper()
	k, err := New(secret, project)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealedDataOpensUnderTheSameKeyProjectAndUse(t *testing.T) {
	k := keys(t, secret, "shop")
	sealed, err := k.Seal(UseCA, []byte("the CA key"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("the CA key")) {
		t.Fatal("the record holds the data in clear")
	}
	plain, err := keys(t, secret, "shop").Open(UseCA, sealed)
	if err != nil || string(plain) != "the CA key" {
		t.Fatalf("Open = %q, %v", plain, err)
	}
	again, _ := k.Seal(UseCA, []byte("the CA key"))
	if bytes.Equal(sealed, again) {
		t.Error("two seals of the same data are equal: the nonce is not random")
	}
}

func TestAnotherKeyOrProjectIsAWrongKeyAndAnotherUseOrATamperedRecordIsCorrupt(t *testing.T) {
	sealed, _ := keys(t, secret, "shop").Seal(UseCA, []byte("data"))
	other := []byte(strings.Repeat("o", MinKeyBytes))
	if _, err := keys(t, other, "shop").Open(UseCA, sealed); !errors.Is(err, ErrWrongKey) {
		t.Errorf("another key: %v", err)
	}
	if _, err := keys(t, secret, "blog").Open(UseCA, sealed); !errors.Is(err, ErrWrongKey) {
		t.Errorf("another project, the same secret key: %v", err)
	}
	if _, err := keys(t, secret, "shop").Open(UseOAuth, sealed); !errors.Is(err, ErrCorrupt) {
		t.Errorf("another use: %v", err)
	}
	tampered := bytes.Replace(sealed, []byte(`"data":"`), []byte(`"data":"AAAA`), 1)
	if _, err := keys(t, secret, "shop").Open(UseCA, tampered); !errors.Is(err, ErrCorrupt) {
		t.Errorf("tampered: %v", err)
	}
	if _, err := keys(t, secret, "shop").Open(UseCA, []byte("not a record")); !errors.Is(err, ErrCorrupt) {
		t.Errorf("garbage: %v", err)
	}
}

func TestASecretKeyIsAtLeast32Bytes(t *testing.T) {
	if _, err := New([]byte(strings.Repeat("k", MinKeyBytes-1)), "shop"); err == nil {
		t.Error("a short key was accepted")
	}
}
