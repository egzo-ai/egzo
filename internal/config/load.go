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

// EnvFile names the project file, like -f.
const EnvFile = "EGZO_FILE"

// Load reads and strictly decodes egzo.yaml from dir.
func Load(dir string) (*File, error) { return LoadFile(filepath.Join(dir, FileName)) }

// LoadFile reads and strictly decodes the project file at path.
func LoadFile(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no such project file: %s", path)
		}
		return nil, err
	}
	return Parse(data)
}

// FindFile locates the project file the way Docker Compose does: the directory you are in, then each
// parent, up to the root. It returns the file's absolute path; the project directory is the directory
// that holds it, wherever you ran egzo from.
func FindFile(from string) (string, error) {
	dir, err := filepath.Abs(from)
	if err != nil {
		return "", err
	}
	for {
		path := filepath.Join(dir, FileName)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s in %s or any parent directory (use -f to name the file)", FileName, from)
		}
		dir = parent
	}
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

// rejectUsers enforces that users, roles and grants are never declared in project files: they live
// in the hub. Only the places a users section could be declared are checked (the top level and the
// settings of an agent, profile, workspace or vault), not every key that happens to be named users:
// a workspace, a variable or a secret may be called that.
func rejectUsers(root *yaml.Node) error {
	node := root
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	check := func(mapping *yaml.Node) error {
		for i := 0; mapping.Kind == yaml.MappingNode && i+1 < len(mapping.Content); i += 2 {
			if key := mapping.Content[i]; key.Value == "users" {
				return fmt.Errorf("%s:%d: users are never declared in project files; user management lives in the hub", FileName, key.Line)
			}
		}
		return nil
	}
	if err := check(node); err != nil {
		return err
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		switch node.Content[i].Value {
		case "agents", "egress", "workspaces", "vaults":
			section := node.Content[i+1]
			for j := 0; section.Kind == yaml.MappingNode && j+1 < len(section.Content); j += 2 {
				if err := check(section.Content[j+1]); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
