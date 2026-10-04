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
