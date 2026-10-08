// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package config

import (
	"errors"
	"fmt"
	"strings"
)

// problems collects validation errors so one run reports everything that is wrong.
type problems struct {
	list []string
}

func (p *problems) addf(format string, args ...any) {
	p.list = append(p.list, fmt.Sprintf(format, args...))
}

func (p *problems) err() error {
	if len(p.list) == 0 {
		return nil
	}
	return errors.New(strings.Join(p.list, "\n"))
}

// suggest returns the candidate closest to name, if any is close enough to be a likely typo.
func suggest(name string, candidates []string) string {
	best, bestDistance := "", 3
	for _, candidate := range candidates {
		if d := editDistance(name, candidate); d < bestDistance {
			best, bestDistance = candidate, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	previous := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current := make([]int, len(b)+1)
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous = current
	}
	return previous[len(b)]
}
