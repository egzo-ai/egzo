// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package stack

import "testing"

const (
	debianPasswd = "root:x:0:0:root:/root:/bin/bash\nnode:x:1000:1000::/home/node:/bin/bash\n"
	debianGroup  = "root:x:0:\nnode:x:1000:\n"
)

func TestWithUserEntry(t *testing.T) {
	cases := []struct {
		name, user, home      string
		passwd, group         string
		wantPasswd, wantGroup string
		wantChanged           bool
	}{
		{"unknown uid is added with the home", "1234:1234", "/home/agent", debianPasswd, debianGroup,
			debianPasswd + "agent:x:1234:1234:agent:/home/agent:/bin/sh\n", debianGroup + "agent:x:1234:\n", true},
		{"no home means /", "1234:1234", "", debianPasswd, debianGroup,
			debianPasswd + "agent:x:1234:1234:agent:/:/bin/sh\n", debianGroup + "agent:x:1234:\n", true},
		{"uid alone gets group 0", "1234", "/h", debianPasswd, debianGroup,
			debianPasswd + "agent:x:1234:0:agent:/h:/bin/sh\n", debianGroup, true},
		{"a known uid keeps its entry but takes the home", "1000:1000", "/home/agent", debianPasswd, debianGroup,
			"root:x:0:0:root:/root:/bin/bash\nnode:x:1000:1000::/home/agent:/bin/bash\n", debianGroup, true},
		{"a known uid with the right home changes nothing", "1000:1000", "/home/node", debianPasswd, debianGroup, debianPasswd, debianGroup, false},
		{"a known uid with no home given changes nothing", "1000:1000", "", debianPasswd, debianGroup, debianPasswd, debianGroup, false},
		{"no user is root, which gets the home", "", "/home/agent", debianPasswd, debianGroup,
			"root:x:0:0:root:/home/agent:/bin/bash\nnode:x:1000:1000::/home/node:/bin/bash\n", debianGroup, true},
		{"a user by name", "node", "/home/agent", debianPasswd, debianGroup,
			"root:x:0:0:root:/root:/bin/bash\nnode:x:1000:1000::/home/agent:/bin/bash\n", debianGroup, true},
		{"a name the image does not have is left to the engine", "nobody-here", "/h", debianPasswd, debianGroup, debianPasswd, debianGroup, false},
		{"the name agent is taken by another uid", "1234:1234", "/h", "agent:x:5:5::/x:/bin/sh\n", "",
			"agent:x:5:5::/x:/bin/sh\nagent1234:x:1234:1234:agent1234:/h:/bin/sh\n", "agent:x:1234:\n", true},
		{"a group by name", "1234:node", "/h", debianPasswd, debianGroup,
			debianPasswd + "agent:x:1234:1000:agent:/h:/bin/sh\n", debianGroup, true},
		{"a missing group file is made", "0:0", "", "root:x:0:0:root:/root:/bin/sh\n", "",
			"root:x:0:0:root:/root:/bin/sh\n", "agent:x:0:\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			passwd, group, changed := withUserEntry([]byte(c.passwd), []byte(c.group), c.user, c.home)
			if string(passwd) != c.wantPasswd || string(group) != c.wantGroup || changed != c.wantChanged {
				t.Errorf("passwd:\n%s\ngroup:\n%s\nchanged %v\nwant passwd:\n%s\ngroup:\n%s\nchanged %v", passwd, group, changed, c.wantPasswd, c.wantGroup, c.wantChanged)
			}
		})
	}
}
