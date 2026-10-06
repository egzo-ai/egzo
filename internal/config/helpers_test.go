package config

import "testing"

func TestParseRef(t *testing.T) {
	cases := []struct {
		raw      string
		ok       bool
		kind     refKind
		target   string
		agent    string // unused: kept so the table reads as before
		readOnly bool
	}{
		{"repo", true, refWorkspace, "repo", "", false},
		{"repo:ro", true, refWorkspace, "repo", "", true},
		{"repo:rw", true, refWorkspace, "repo", "", false},
		{"./docs:ro", true, refHost, "./docs", "", true},
		{"../shared", true, refHost, "../shared", "", false},
		{"/srv/data", true, refHost, "/srv/data", "", false},
		{"coder/repo:ro", false, 0, "", "", false}, // another agent's checkout cannot be mounted
		{"coder/repo", false, 0, "", "", false},
		{"", false, 0, "", "", false},
		{":ro", false, 0, "", "", false},
		{"a:b:ro", false, 0, "", "", false},
		{"a/b/c:ro", false, 0, "", "", false},
		{"/b:ro:x", false, 0, "", "", false},
	}
	for _, c := range cases {
		got, ok := parseRef(c.raw)
		if ok != c.ok {
			t.Errorf("parseRef(%q) ok = %v, want %v", c.raw, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if got.kind != c.kind || got.target != c.target || got.readOnly != c.readOnly {
			t.Errorf("parseRef(%q) = %+v", c.raw, got)
		}
	}
}

func TestProjectNamePrecedence(t *testing.T) {
	file := "from-file"
	cases := []struct {
		name      string
		flag, env string
		file      *string
		dir, want string
		wantError bool
	}{
		{"flag wins", "Flag", "env", &file, "/x/dir", "flag", false},
		{"env beats file", "", "env", &file, "/x/dir", "env", false},
		{"file beats directory", "", "", &file, "/x/dir", "from-file", false},
		{"directory is the fallback", "", "", nil, "/x/My Project.v2", "my-project-v2", false},
		{"directory with only symbols", "", "", nil, "/x/___", "", true},
		{"explicit names are not normalized", "bad name", "", nil, "/x/dir", "", true},
	}
	for _, c := range cases {
		got, err := ProjectName(c.flag, c.env, c.file, c.dir)
		if (err != nil) != c.wantError || got != c.want {
			t.Errorf("%s: ProjectName = %q, %v; want %q (error %v)", c.name, got, err, c.want, c.wantError)
		}
	}
}

func TestSuggest(t *testing.T) {
	known := []string{"anthropic", "github"}
	if got := suggest("antropic", known); got != "anthropic" {
		t.Errorf("suggest(antropic) = %q", got)
	}
	if got := suggest("kubernetes", known); got != "" {
		t.Errorf("suggest(kubernetes) = %q, want no suggestion", got)
	}
}

func TestMatchHost(t *testing.T) {
	cases := []struct {
		pattern, host string
		want          bool
	}{
		{"*", "anything.example", true},
		{"example.com", "example.com", true},
		{"example.com", "sub.example.com", false},
		{"*.pypi.org", "files.pypi.org", true},
		{"*.pypi.org", "pypi.org", false},
		{"*.pypi.org", "evilpypi.org", false},
	}
	for _, c := range cases {
		if got := MatchHost(c.pattern, c.host); got != c.want {
			t.Errorf("MatchHost(%q, %q) = %v", c.pattern, c.host, got)
		}
	}
}

func TestValidAllow(t *testing.T) {
	for _, ok := range []string{"*", "example.com", "*.example.com", "a-b.example.com"} {
		if !validAllow(ok) {
			t.Errorf("validAllow(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "https://example.com", "exa mple.com", "*example.com", "a.*.com", "example.com/path", "example.com:443"} {
		if validAllow(bad) {
			t.Errorf("validAllow(%q) = true", bad)
		}
	}
}

func TestStrictDecodingNamesTheUnknownKey(t *testing.T) {
	if _, err := Parse([]byte("nonsense: 1\n")); err == nil {
		t.Fatal("unknown top-level key accepted")
	}
	if _, err := Parse([]byte("agents:\n  a:\n    harness: custom\n    users: [x]\n")); err == nil {
		t.Fatal("users accepted inside an agent")
	}
	if _, err := Parse([]byte("")); err != nil {
		t.Fatalf("empty file rejected: %v", err)
	}
}
