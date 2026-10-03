package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// FileName is the project file egzo looks for in the project directory.
const FileName = "egzo.yaml"

// Load reads and strictly decodes egzo.yaml from dir.
func Load(dir string) (*File, error) {
	path := filepath.Join(dir, FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no %s in %s", FileName, dir)
		}
		return nil, err
	}
	return Parse(data)
}

// Parse strictly decodes a project file: unknown keys are errors.
func Parse(data []byte) (*File, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	if err := rejectUsers(&root); err != nil {
		return nil, err
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var file File
	if err := decoder.Decode(&file); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	return &file, nil
}

// rejectUsers enforces that users, roles and grants are never declared in project files:
// they live in the hub.
func rejectUsers(node *yaml.Node) error {
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Value == "users" {
				return fmt.Errorf("%s:%d: users are never declared in project files; user management lives in the hub", FileName, key.Line)
			}
			if err := rejectUsers(node.Content[i+1]); err != nil {
				return err
			}
		}
		return nil
	}
	for _, child := range node.Content {
		if err := rejectUsers(child); err != nil {
			return err
		}
	}
	return nil
}
