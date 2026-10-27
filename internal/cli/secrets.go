// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"errors"
	"sort"
	"strings"

	"github.com/egzo-ai/egzo/internal/config"
	"github.com/egzo-ai/egzo/internal/stack"
)

func sortedSecretRefs(p *project) []string {
	refs := make([]string, 0, len(p.Resolved.Secrets))
	for ref := range p.Resolved.Secrets {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

// secretState is what `project check` says about a secret, with the reason when it cannot be used. The error is
// only for a failure that makes every answer meaningless: `pass` itself cannot be run.
func secretState(ref config.SecretRef, dir string, secretKey bool) (string, error) {
	read := stack.ReadSecret
	if secretKey {
		read = stack.ReadSecretKey
	}
	_, err := read(ref, dir)
	var passErr *stack.PassError
	var short *stack.ShortSecretKeyError
	switch {
	case err == nil:
		return "set", nil
	case errors.As(err, &short):
		return short.Error(), nil
	case errors.Is(err, stack.ErrPassNotFound):
		return "", err
	case errors.Is(err, stack.ErrEmptySecret):
		return "empty", nil
	case errors.Is(err, stack.ErrNotASecret):
		return "missing (" + firstLine(err.Error()) + ")", nil
	case errors.As(err, &passErr):
		return "missing (" + passErr.Message + ")", nil
	case strings.Contains(err.Error(), "is not set"):
		return "missing", nil
	}
	return "unreadable (" + firstLine(err.Error()) + ")", nil
}
