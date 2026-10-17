// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package config

import (
	"strings"
	"testing"
)

// wantRejected: the file is refused, whether by the strict parse or by resolution.
func wantRejected(t *testing.T, yaml, fragment string) {
	t.Helper()
	file, err := Parse([]byte(yaml))
	if err == nil {
		_, _, err = Resolve(file, "proj", t.TempDir())
	}
	if err == nil || !strings.Contains(err.Error(), fragment) {
		t.Errorf("want an error with %q, got %v", fragment, err)
	}
}

const sshFixture = `
vaults:
  main:
    backend: env
    secrets: [KEY]
`

const goodFingerprint = "SHA256:giDwc8qRcN2ytSupvU/QCL4YGA5l7fUGresaokyLYt4"

func sshProfile(extra string) string {
	return sshFixture + `
egress:
  default:
    ssh:
      prod:
        host: Prod.Example.com.
        user: deploy
        key: main/KEY
        host_key: ` + goodFingerprint + extra + `
`
}

func TestSSHServerResolves(t *testing.T) {
	resolved, _ := mustResolve(t, t.TempDir(), sshProfile(""))
	got := resolved.Egress["default"].SSH["prod"]
	want := ResolvedSSH{Host: "prod.example.com", Port: 22, User: "deploy", Key: "main/KEY", HostKey: goodFingerprint}
	if got == nil || *got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestSSHPort(t *testing.T) {
	resolved, _ := mustResolve(t, t.TempDir(), sshProfile("\n        port: 2222"))
	if port := resolved.Egress["default"].SSH["prod"].Port; port != 2222 {
		t.Fatalf("port = %d", port)
	}
	for _, port := range []string{"0", "65536", "-1", "ssh", "22.5"} {
		wantRejected(t, sshProfile("\n        port: "+port), "port")
	}
}

func TestSSHHostKeyForms(t *testing.T) {
	public := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMGFJIY0eOCEYAre+qhPAFyN2YPymAD03FxBn1v6yyZz comment"
	resolved, _ := mustResolve(t, t.TempDir(), sshFixture+`
egress:
  default:
    ssh:
      prod: {host: h, user: u, key: main/KEY, host_key: "`+public+`"}
`)
	if resolved.Egress["default"].SSH["prod"].HostKey != public {
		t.Fatal("a public key line is kept as given")
	}
	for _, bad := range []string{"", "SHA256:short", "MD5:aa", "ssh-ed25519", "ssh-ed25519 !!!", goodFingerprint + "A", "tofu"} {
		wantProblems(t, t.TempDir(), sshFixture+`
egress:
  default:
    ssh:
      prod: {host: h, user: u, key: main/KEY, host_key: "`+bad+`"}
`, "host_key")
	}
}

func TestSSHInvalidFields(t *testing.T) {
	cases := map[string]string{
		"host: '*.example.com'":    "host",
		"host: 'a:22'":             "host",
		"user: '-oProxyCommand=x'": "user",
		"user: 'a b'":              "user",
		"key: main/MISSING":        "MISSING",
		"key: nope/KEY":            "nope",
		"key: bare":                "bare",
		"password: x":              "password",
		"forward_agent: true":      "forward_agent",
	}
	for field, fragment := range cases {
		key := field[:indexByte(field, ':')]
		base := map[string]string{"host": "h", "user": "u", "key": "main/KEY", "host_key": goodFingerprint}
		delete(base, key)
		doc := sshFixture + "\negress:\n  default:\n    ssh:\n      prod:\n        " + field + "\n"
		for k, v := range base {
			doc += "        " + k + ": " + v + "\n"
		}
		wantRejected(t, doc, fragment)
	}
}

func indexByte(s string, c byte) int {
	for i := range s {
		if s[i] == c {
			return i
		}
	}
	return len(s)
}

func TestSSHNames(t *testing.T) {
	for _, name := range []string{"Prod", "-prod", "pro d", "root"} {
		wantProblems(t, t.TempDir(), sshFixture+"\negress:\n  default:\n    ssh:\n      \""+name+"\": {host: h, user: u, key: main/KEY, host_key: "+goodFingerprint+"}\n", "ssh server")
	}
}

func TestSSHExtendMergesByNameChildWins(t *testing.T) {
	resolved, _ := mustResolve(t, t.TempDir(), sshFixture+`
egress:
  default:
    ssh:
      prod: {host: a, user: u, key: main/KEY, host_key: `+goodFingerprint+`}
      db: {host: d, user: u, key: main/KEY, host_key: `+goodFingerprint+`}
  ops:
    extend: default
    ssh:
      prod: {host: b, user: admin, key: main/KEY, host_key: `+goodFingerprint+`}
`)
	ops := resolved.Egress["ops"].SSH
	if len(ops) != 2 || ops["prod"].Host != "b" || ops["prod"].User != "admin" || ops["db"].Host != "d" {
		t.Fatalf("ops = %+v", ops)
	}
	if resolved.Egress["default"].SSH["prod"].Host != "a" {
		t.Fatal("the parent changed")
	}
}
