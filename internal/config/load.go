// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

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

var lineError = regexp.MustCompile(`^(?:yaml: )?line (\d+): (.*)$`)

// Parse strictly decodes a project file: an unknown key is an error, and every problem found is reported, not
// only the first (see Errors).
func Parse(data []byte) (*File, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, Errors{locate(err.Error()).String()}
	}
	var found []located
	if len(root.Content) > 0 {
		unknownKeys(root.Content[0], reflect.TypeOf(File{}), "", &found)
	}
	var file File
	if err := yaml.Unmarshal(data, &file); err != nil {
		var typeErr *yaml.TypeError
		if errors.As(err, &typeErr) {
			for _, message := range typeErr.Errors {
				found = append(found, locate(message))
			}
		} else {
			found = append(found, locate(err.Error()))
		}
	}
	if len(found) == 0 {
		return &file, nil
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].line != found[j].line {
			return found[i].line < found[j].line
		}
		return found[i].column < found[j].column
	})
	errs := make(Errors, len(found))
	for i, f := range found {
		errs[i] = f.String()
	}
	return nil, errs
}

// locate turns a yaml message such as `line 5: cannot unmarshal ...` into the shape of every other error of the file.
func locate(message string) located {
	message = strings.TrimPrefix(message, "yaml: ")
	if m := lineError.FindStringSubmatch(message); m != nil {
		line, _ := strconv.Atoi(m[1])
		return located{line: line, text: m[2]}
	}
	return located{text: message}
}
