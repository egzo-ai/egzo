package auth_test

import (
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/hub/internal/auth"
)

func TestPasswordsAreHashedWithArgon2idAndChecked(t *testing.T) {
	hash, err := auth.HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=4$") || strings.Contains(hash, "correct horse") {
		t.Errorf("hash = %q", hash)
	}
	if again, _ := auth.HashPassword("correct horse"); again == hash {
		t.Error("two hashes of one password are equal: no salt")
	}
	if ok, err := auth.CheckPassword(hash, "correct horse"); !ok || err != nil {
		t.Errorf("the right password: %v %v", ok, err)
	}
	if ok, _ := auth.CheckPassword(hash, "wrong"); ok {
		t.Error("a wrong password matched")
	}
	if _, err := auth.CheckPassword("$2a$10$bcrypt", "x"); err == nil {
		t.Error("another kind of hash was accepted")
	}
}

func TestTheParametersAreReadFromTheHash(t *testing.T) {
	// a hash made with other parameters (m=1024, t=1, p=1) still checks: they can be raised later
	const old = "$argon2id$v=19$m=1024,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$QNAQ5+BvxoMEi1bFbwKlOodniiVsGsVeHmPJBJ2Av+Y"
	if ok, err := auth.CheckPassword(old, "pw"); err != nil || !ok {
		t.Errorf("old parameters: %v %v", ok, err)
	}
}

func TestNewPasswordsAreLongAndDiffer(t *testing.T) {
	a, b := auth.NewPassword(), auth.NewPassword()
	if a == b || len(a) < 24 {
		t.Errorf("%q %q", a, b)
	}
}
