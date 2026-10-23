// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package engine

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egzo-ai/egzo/internal/names"
)

// createdBy is the form of the created-by label: egzo and its version.
var createdBy = regexp.MustCompile(`^egzo/[A-Za-z0-9._+-]{1,64}$`)

// maxProjectDir is the longest project directory a label holds: PATH_MAX.
const maxProjectDir = 4096

// ErrLabel is the label of a resource that egzo never writes in that form.
type ErrLabel struct{ Key, Value string }

func (e ErrLabel) Error() string {
	return fmt.Sprintf("label %s is %.60q, which egzo never writes", e.Key, e.Value)
}

// IdentityOf reads the labels of a resource the way egzo wrote them (Identity.Labels). It is how everything that reads an engine,
// the CLI and the hub, tells what egzo made from what only claims to be: a label that egzo never writes in that form is an
// ErrLabel, and the resource is not egzo's. The project and the kind are required; the other labels are checked when they are
// there. The semantic labels (Extra) are not read.
func IdentityOf(labels map[string]string) (Identity, error) {
	id := Identity{
		Project: labels[LabelProject], Service: labels[LabelService], Kind: labels[LabelKind], ProjectDir: labels[LabelProjectDir],
		ConfigHash: labels[LabelConfigHash], Instance: labels[LabelInstance], Actor: labels[LabelActor], TemplateHash: labels[LabelTemplateHash],
		InstanceID: labels[LabelInstanceID], Workspace: labels[LabelWorkspace], CreatedBy: labels[LabelCreatedBy],
	}
	optional := func(value string, valid func(string) bool) bool { return value == "" || valid(value) }
	for _, check := range []struct {
		key string
		ok  bool
	}{
		{LabelProject, names.Project(id.Project)},
		{LabelKind, names.Identifier(id.Kind)},
		{LabelService, optional(id.Service, names.Identifier)},
		{LabelProjectDir, optional(id.ProjectDir, plainPath)},
		{LabelConfigHash, optional(id.ConfigHash, names.Hash)},
		{LabelInstance, optional(id.Instance, names.Instance)},
		{LabelActor, optional(id.Actor, func(s string) bool { return names.Actor(s, "user", "service") })},
		{LabelTemplateHash, optional(id.TemplateHash, names.Hash)},
		{LabelInstanceID, optional(id.InstanceID, names.InstanceID)},
		{LabelWorkspace, optional(id.Workspace, names.Identifier)},
		{LabelCreatedBy, optional(id.CreatedBy, createdBy.MatchString)},
	} {
		if !check.ok {
			return Identity{}, ErrLabel{Key: check.key, Value: labels[check.key]}
		}
	}
	return id, nil
}

// plainPath reports whether s can be the directory of a project file as egzo labels it: text, with no control character.
func plainPath(s string) bool {
	return len(s) <= maxProjectDir && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
