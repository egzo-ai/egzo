// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// mergeFile merges a JSON object into the file at path (which may not exist).
func mergeFile(path string, ours []byte) ([]byte, error) {
	existing, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || len(bytes.TrimSpace(existing)) == 0 {
		existing = []byte("{}")
	} else if err != nil {
		return nil, err
	}
	var probe map[string]any
	if json.Unmarshal(existing, &probe) != nil || probe == nil {
		// The harness's own state (its history, what it was told to trust) is not ours to discard:
		// keep the damaged file next to the new one, and say so.
		if err := os.WriteFile(path+".corrupt", existing, 0o600); err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "egzo: %s was not valid JSON; it is kept as %s.corrupt and a new one is written\n", path, path)
		existing = []byte("{}")
	}
	return MergeJSON(existing, ours)
}

// MergeJSON merges the JSON object ours into the JSON object theirs: objects merge key by key,
// lists of strings are united (theirs first), null deletes a key, anything else is replaced. A file
// that is not a JSON object is replaced.
func MergeJSON(theirs, ours []byte) ([]byte, error) {
	var base, overlay map[string]any
	if err := decodeExact(theirs, &base); err != nil || base == nil {
		base = map[string]any{}
	}
	if err := decodeExact(ours, &overlay); err != nil {
		return nil, err
	}
	merged := mergeObjects(base, overlay)
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

func mergeObjects(base, overlay map[string]any) map[string]any {
	for key, value := range overlay {
		switch v := value.(type) {
		case nil:
			delete(base, key)
		case map[string]any:
			if existing, ok := base[key].(map[string]any); ok {
				base[key] = mergeObjects(existing, v)
			} else {
				base[key] = mergeObjects(map[string]any{}, v)
			}
		case []any:
			if existing, ok := base[key].([]any); ok && allStrings(existing) && allStrings(v) {
				base[key] = unite(existing, v)
			} else {
				base[key] = v
			}
		default:
			base[key] = value
		}
	}
	return base
}

func allStrings(list []any) bool {
	for _, item := range list {
		if _, ok := item.(string); !ok {
			return false
		}
	}
	return true
}

func unite(a, b []any) []any {
	seen := map[string]bool{}
	var out []any
	for _, list := range [][]any{a, b} {
		for _, item := range list {
			if text := item.(string); !seen[text] {
				seen[text] = true
				out = append(out, text)
			}
		}
	}
	return out
}

// decodeExact decodes JSON keeping numbers as written (json.Number), so an integer larger than a
// float64 holds exactly survives a merge.
func decodeExact(data []byte, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(into)
}
