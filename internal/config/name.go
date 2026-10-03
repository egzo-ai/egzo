package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// EnvProjectName is the environment variable that overrides the name in the file.
const EnvProjectName = "EGZO_PROJECT_NAME"

// ProjectName resolves the project name like docker-compose: the flag, then the environment,
// then name: in the file, then the name of the project directory.
//
// Explicit names are lowercased and must then be valid. A name derived from the directory is
// normalized instead, because the user did not choose it.
func ProjectName(flag, env string, file *string, dir string) (string, error) {
	switch {
	case flag != "":
		return explicitName("flag --project-name", flag)
	case env != "":
		return explicitName("environment "+EnvProjectName, env)
	case file != nil:
		return explicitName("name in "+FileName, *file)
	}
	return directoryName(dir)
}

func explicitName(source, value string) (string, error) {
	name := strings.ToLower(value)
	if !validName.MatchString(name) {
		return "", fmt.Errorf("invalid project name %q from %s: use lowercase letters, digits, '-' and '_', starting with a letter or digit", value, source)
	}
	return name, nil
}

func directoryName(dir string) (string, error) {
	base := strings.ToLower(filepath.Base(dir))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	name := strings.TrimLeft(b.String(), "-_")
	if name == "" {
		return "", fmt.Errorf("cannot derive a project name from directory %q: set name: in %s or use --project-name", dir, FileName)
	}
	return name, nil
}
