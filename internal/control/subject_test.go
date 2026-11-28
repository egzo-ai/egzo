// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package control

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestASubjectIsOneLineOfPlainText(t *testing.T) {
	for subject, want := range map[string]bool{
		"Review branch x":           true,
		"Déployer en préproduction": true,
		"":                          false,
		"   ":                       false,
		"two\nlines":                false,
		"a \x1b[31mred\x1b[0m one":  false,
		strings.Repeat("x", 120):    true,
		strings.Repeat("x", 121):    false,
		strings.Repeat("é", 120):    true,
		"bad \xff utf-8":            false,
	} {
		if validSubject(subject) != want {
			t.Errorf("%q: %v", subject, !want)
		}
	}
}

func TestASubjectNobodyGaveIsTheStartOfTheFirstLine(t *testing.T) {
	for text, want := range map[string]string{
		"fix the typo\nin the README":       "fix the typo",
		"  \n  spaced   out \t words  \nno": "spaced out words",
		"a \x1b[31mred\x1b[0m one":          "a [31mred [0m one",
	} {
		if got := subjectOf(text); got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
	long := strings.Repeat("word ", 60)
	got := subjectOf(long)
	if utf8.RuneCountInString(got) > maxSubject || !strings.HasSuffix(got, "…") || !strings.HasPrefix(long, strings.TrimSuffix(got, "…")) ||
		strings.HasSuffix(strings.TrimSuffix(got, "…"), " ") {
		t.Errorf("a long first line is cut at a word: %q", got)
	}
	if !validSubject(got) {
		t.Errorf("what egzo takes is a valid subject: %q", got)
	}
}

func TestWhatAnswersARequestIsReItsSubjectOnce(t *testing.T) {
	if got := reSubject("Deploy"); got != "Re: Deploy" {
		t.Error(got)
	}
	if got := reSubject("Re: Deploy"); got != "Re: Deploy" {
		t.Error(got)
	}
}

func TestEveryMessageHasASubjectAndWhatAnswersCarriesTheRequests(t *testing.T) {
	r := newRig(t)
	r.project("coder", "reviewer")
	if _, err := r.srv.sendFromAgent("coder", "agent:reviewer", "", "review", ""); statusOf(t, err) != 400 {
		t.Errorf("an agent's request without a subject: %v", err)
	}
	id, err := r.srv.sendFromAgent("coder", "agent:reviewer", "Review the parser", "review", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.srv.fetch("reviewer", id); err != nil {
		t.Fatal(err)
	}
	update, _ := r.srv.update("reviewer", id, "reading")
	question, _ := r.srv.ask("reviewer", id, "which file?", nil)
	result, err := r.srv.resolve("reviewer", id, "fine", "done")
	if err != nil {
		t.Fatal(err)
	}
	for _, answer := range []string{update, question, result} {
		if m, _ := r.srv.events.message(answer); m.Subject != "Re: Review the parser" {
			t.Errorf("%s: %q", answer, m.Subject)
		}
	}
	m, _ := r.srv.events.message(id)
	if m.Subject != "Review the parser" {
		t.Errorf("the request: %q", m.Subject)
	}
}
