// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package engine

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/egzo-ai/egzo/internal/version"
)

func instanceIdentity() Identity {
	return Identity{Project: "web", Service: "coder", Kind: "agent", ProjectDir: "/home/ann/web", ConfigHash: strings.Repeat("a", 64),
		Instance: "coder-1", Actor: "user:u_abc123", TemplateHash: strings.Repeat("b", 64), InstanceID: "01JAB3C4D5E6F7G8H9JKMNPQRS", Workspace: "src"}
}

func TestWhatEgzoLabelsIsReadBack(t *testing.T) {
	for _, id := range []Identity{instanceIdentity(), {Project: "web", Service: "control", Kind: "control", ProjectDir: "/w", ConfigHash: strings.Repeat("c", 64)}, {Project: "web", Service: "lock", Kind: "lock"}} {
		got, err := IdentityOf(id.Labels())
		id.CreatedBy = "egzo/" + version.Version
		if err != nil || !reflect.DeepEqual(got, id) {
			t.Errorf("IdentityOf(%+v) = %+v, %v", id, got, err)
		}
	}
}

func TestALabelEgzoNeverWritesMakesTheResourceNotEgzos(t *testing.T) {
	for key, value := range map[string]string{
		LabelProject: "../../etc", LabelKind: "", LabelService: "<img src=x>", LabelProjectDir: "/a\nb", LabelConfigHash: "zz",
		LabelInstance: "Not Egzo's", LabelActor: "root", LabelTemplateHash: strings.Repeat("a", 65), LabelInstanceID: "nope", LabelWorkspace: "a/b", LabelCreatedBy: "someone else",
	} {
		labels := instanceIdentity().Labels()
		labels[key] = value
		_, err := IdentityOf(labels)
		var bad ErrLabel
		if !errors.As(err, &bad) || bad.Key != key {
			t.Errorf("%s=%q: err = %v", key, value, err)
		}
	}
	if _, err := IdentityOf(map[string]string{LabelKind: "agent"}); err == nil {
		t.Error("a resource with no project is egzo's")
	}
}
