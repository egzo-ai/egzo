package harness

import (
	"bytes"
	"encoding/json"
	"errors"
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
	return MergeJSON(existing, ours)
}

// MergeJSON merges the JSON object ours into the JSON object theirs: objects merge key by key,
// lists of strings are united (theirs first), null deletes a key, anything else is replaced. A file
// that is not a JSON object is replaced.
func MergeJSON(theirs, ours []byte) ([]byte, error) {
	var base, overlay map[string]any
	if err := json.Unmarshal(theirs, &base); err != nil || base == nil {
		base = map[string]any{}
	}
	if err := json.Unmarshal(ours, &overlay); err != nil {
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
