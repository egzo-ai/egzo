package stack

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/egzo-ai/egzo/internal/engine"
)

func TestNewSnapshot(t *testing.T) {
	project := desireProject()
	project.SecretSources = map[string]string{"main/KEY": "env:SUPER_SECRET_SOURCE"}
	desired := desire(t, project)

	data, hash, err := newSnapshot(project, desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != 64 {
		t.Errorf("hash = %q, want a sha256 hex digest", hash)
	}

	var decoded Snapshot
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("the snapshot is not valid yaml: %v", err)
	}
	if decoded.SpecVersion != engine.SpecVersion {
		t.Errorf("spec-version = %q", decoded.SpecVersion)
	}
	if decoded.Config == nil || decoded.Config.Name != "proj" {
		t.Errorf("config = %+v", decoded.Config)
	}
	for _, spec := range desired.Containers {
		if decoded.ConfigHashes[spec.Name] != spec.Identity.ConfigHash {
			t.Errorf("config hash of %s = %q, want %q", spec.Name, decoded.ConfigHashes[spec.Name], spec.Identity.ConfigHash)
		}
	}
	if len(decoded.ConfigHashes) != len(desired.Containers) {
		t.Errorf("hashes = %v", decoded.ConfigHashes)
	}
	if strings.Contains(string(data), "SUPER_SECRET_SOURCE") {
		t.Error("the snapshot leaks where secrets come from")
	}

	_, again, _ := newSnapshot(project, desired)
	if again != hash {
		t.Error("the snapshot hash is not deterministic")
	}
}
