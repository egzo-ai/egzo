// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Errors is every problem found in a project file, in file order. Each one reads
// `egzo.yaml:LINE:COL: path: message`, so an editor or a person can go straight to it.
type Errors []string

func (e Errors) Error() string { return strings.Join(e, "\n") }

// located is one problem with the place it was found, so the list can be put in file order.
type located struct {
	line, column int
	text         string
}

func (l located) String() string {
	if l.line == 0 {
		return fmt.Sprintf("%s: %s", FileName, l.text)
	}
	if l.column == 0 {
		return fmt.Sprintf("%s:%d: %s", FileName, l.line, l.text)
	}
	return fmt.Sprintf("%s:%d:%d: %s", FileName, l.line, l.column, l.text)
}

var serviceEntryType = reflect.TypeOf(ServiceEntry{})

// unknownKeys walks the document against the types of the file and reports every key no type has, with the
// path to the mapping that holds it and, for a typo, the key that was meant.
func unknownKeys(node *yaml.Node, t reflect.Type, path string, found *[]located) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == serviceEntryType {
		t = reflect.TypeOf(ServiceDef{}) // a service is a secret reference or a definition; only a definition has keys
	}
	switch {
	case node.Kind == yaml.AliasNode && node.Alias != nil:
		unknownKeys(node.Alias, t, path, found)
	case node.Kind == yaml.MappingNode && t.Kind() == reflect.Struct:
		fields := keysOf(t)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			field, ok := fields[key.Value]
			if !ok {
				*found = append(*found, located{key.Line, key.Column, unknownKeyMessage(path, key.Value, fields)})
				continue
			}
			unknownKeys(value, field, join(path, key.Value), found)
		}
	case node.Kind == yaml.MappingNode && t.Kind() == reflect.Map:
		for i := 0; i+1 < len(node.Content); i += 2 {
			unknownKeys(node.Content[i+1], t.Elem(), join(path, node.Content[i].Value), found)
		}
	case node.Kind == yaml.SequenceNode && t.Kind() == reflect.Slice:
		for i, item := range node.Content {
			unknownKeys(item, t.Elem(), fmt.Sprintf("%s[%d]", path, i), found)
		}
	}
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// keysOf maps each yaml key of a struct to the type of its value.
func keysOf(t reflect.Type) map[string]reflect.Type {
	keys := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if name != "" && name != "-" {
			keys[name] = t.Field(i).Type
		}
	}
	return keys
}

func unknownKeyMessage(path, key string, valid map[string]reflect.Type) string {
	prefix := ""
	if path != "" {
		prefix = path + ": "
	}
	if key == "users" {
		return prefix + "users are never declared in project files; user management lives in the hub"
	}
	names := make([]string, 0, len(valid))
	for name := range valid {
		names = append(names, name)
	}
	sort.Strings(names)
	if guess := suggest(key, names); guess != "" {
		return fmt.Sprintf("%sunknown key %q, did you mean %q?", prefix, key, guess)
	}
	if len(names) == 0 {
		return fmt.Sprintf("%sunknown key %q (this section has no keys)", prefix, key)
	}
	return fmt.Sprintf("%sunknown key %q (valid keys: %s)", prefix, key, strings.Join(names, ", "))
}
