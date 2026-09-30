package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

// committed is the generated file `make notices` writes, from this package.
const committed = "../../../THIRD_PARTY_NOTICES"

// repoRoot is the repository the test runs in.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// collected is what the binary is made of, worked out again from `go list`.
func collected(t *testing.T) []part {
	t.Helper()
	parts, err := collect(repoRoot(t))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	return parts
}

// TestCommittedNoticesAreWhatTheBinaryLinks works the parts out again from
// `go list` and holds the committed file against them: a module added,
// dropped or bumped without `make notices` fails here, and says what is
// wrong.
func TestCommittedNoticesAreWhatTheBinaryLinks(t *testing.T) {
	text, err := os.ReadFile(committed)
	if err != nil {
		t.Fatalf("no committed notices; run `make notices`: %v", err)
	}
	parts := collected(t)
	if found := problems(string(text), parts); len(found) != 0 {
		t.Fatalf("%s disagrees with the binary; run `make notices`:\n  %s", committed, strings.Join(found, "\n  "))
	}
	var fresh bytes.Buffer
	if err := render(&fresh, parts); err != nil {
		t.Fatal(err)
	}
	if fresh.String() != string(text) {
		t.Fatalf("%s is not what `make notices` writes now", committed)
	}
}

// TestNoticesCoverTheModulesGoModRequires pins what is listed without a
// version written down here: projmux, Go at go.mod's toolchain, and the
// modules the command links at the versions go.mod requires them at. A
// module only a test, a tool or another platform needs is not listed.
func TestNoticesCoverTheModulesGoModRequires(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	mod, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]string{}
	for _, req := range mod.Require {
		required[req.Mod.Path] = req.Mod.Version
	}
	parts := collected(t)
	licenses := map[string]string{}
	for _, p := range parts {
		licenses[p.name] = p.license
		switch p.name {
		case "projmux":
			if p.version != "" || p.license != "MIT" {
				t.Errorf("projmux = %q (%s), want no version and MIT", p.version, p.license)
			}
		case "Go":
			if want := strings.TrimPrefix(mod.Toolchain.Name, "go"); p.version != want || p.license != "BSD-3-Clause" {
				t.Errorf("Go = %s (%s), want go.mod's toolchain %s and BSD-3-Clause", p.version, p.license, want)
			}
		default:
			if required[p.name] != p.version {
				t.Errorf("%s is listed at %s, go.mod requires %q", p.name, p.version, required[p.name])
			}
		}
		if len(p.text) < 500 {
			t.Errorf("%s has no licence text to speak of", p.name)
		}
	}
	want := map[string]string{
		"projmux":                "MIT",
		"Go":                     "BSD-3-Clause",
		"github.com/spf13/cobra": "Apache-2.0",
		"github.com/spf13/pflag": "BSD-3-Clause",
		"golang.org/x/mod":       "BSD-3-Clause",
		"golang.org/x/sys":       "BSD-3-Clause",
		"golang.org/x/term":      "BSD-3-Clause",
	}
	for name, license := range want {
		if licenses[name] != license {
			t.Errorf("%s = %q, want %s", name, licenses[name], license)
		}
	}
	// In go.mod, and not in the binary: windows only, or a tool's.
	for _, name := range []string{"github.com/inconshreveable/mousetrap", "golang.org/x/tools", "golang.org/x/sync", "golang.org/x/telemetry"} {
		if _, ok := licenses[name]; ok {
			t.Errorf("%s is listed, and no supported binary links it", name)
		}
		if _, ok := required[name]; !ok {
			t.Errorf("%s is no longer in go.mod; this check has nothing to show", name)
		}
	}
	if len(parts) != len(want) {
		names := make([]string, 0, len(licenses))
		for name := range licenses {
			names = append(names, name)
		}
		slices.Sort(names)
		t.Errorf("the binary has %d parts (%v); name each new one here with its licence", len(parts), names)
	}
}

// TestProblemsNameWhatIsWrong shows the check failing: a notices text with
// one module's section cut out, one for a module the binary does not link,
// another version, another licence, a section twice, and an edited text.
func TestProblemsNameWhatIsWrong(t *testing.T) {
	parts := collected(t)
	var full bytes.Buffer
	if err := render(&full, parts); err != nil {
		t.Fatal(err)
	}
	if found := problems(full.String(), parts); len(found) != 0 {
		t.Fatalf("the rendered text has problems: %v", found)
	}
	without := func(name string) []part {
		return slices.DeleteFunc(slices.Clone(parts), func(p part) bool { return p.name == name })
	}
	text := func(parts []part) string {
		var out bytes.Buffer
		if err := render(&out, parts); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	heading := func(name string) part {
		return parts[slices.IndexFunc(parts, func(p part) bool { return p.name == name })]
	}
	pflag, sys := heading("github.com/spf13/pflag"), heading("golang.org/x/sys").heading()
	bumped := slices.Clone(parts)
	relicensed := slices.Clone(parts)
	for i := range parts {
		if parts[i].name == "golang.org/x/sys" {
			bumped[i].version = "v0.99.0"
			relicensed[i].license = "MIT"
		}
	}
	cases := []struct {
		name  string
		text  string
		parts []part
		want  []string
	}{
		{"a module's section cut out", text(without("github.com/spf13/pflag")), parts,
			[]string{"missing: " + pflag.heading() + " (BSD-3-Clause) is in the binary and has no notice"}},
		{"a section for a module the binary does not link", full.String(), without("github.com/spf13/pflag"),
			[]string{"extra: " + pflag.heading() + " (BSD-3-Clause) has a notice and is not in the binary"}},
		{"a module bumped", full.String(), bumped,
			[]string{"missing: golang.org/x/sys v0.99.0 (BSD-3-Clause) is in the binary and has no notice",
				"extra: " + sys + " (BSD-3-Clause) has a notice and is not in the binary"}},
		{"a licence that changed", full.String(), relicensed,
			[]string{"missing: " + sys + " (MIT) is in the binary and has no notice",
				"extra: " + sys + " (BSD-3-Clause) has a notice and is not in the binary"}},
		{"a section twice", full.String() + text([]part{pflag})[strings.Index(text([]part{pflag}), rule)-1:], parts,
			[]string{"twice: " + pflag.heading() + " (BSD-3-Clause)"}},
		{"a licence text edited", strings.Replace(full.String(), "Permission is hereby granted", "Permission is granted", 1), parts,
			[]string{"stale: the text is not what these parts render to"}},
		{"nothing at all", "", parts[:1], []string{"missing: projmux (MIT) is in the binary and has no notice"}},
	}
	for _, c := range cases {
		got := problems(c.text, c.parts)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: problems = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestIdentifyNamesOnlyWhatItRecognises: a licence is named by its own
// words, and an unknown text is not guessed at, which stops the generator.
func TestIdentifyNamesOnlyWhatItRecognises(t *testing.T) {
	for text, want := range map[string]string{
		"                                 Apache License\n                           Version 2.0, January 2004\n":                       "Apache-2.0",
		"Redistribution and use in source and binary forms, with or without\nmodification ...\n   * Neither the name of Google LLC nor": "BSD-3-Clause",
		"Permission is hereby granted, free of charge, to any person ...\nTHE SOFTWARE IS PROVIDED \"AS IS\", WITHOUT":                  "MIT",
		"Redistribution and use in source and binary forms, with or without modification":                                               "",
		"GNU GENERAL PUBLIC LICENSE Version 3": "",
		"":                                     "",
	} {
		if got := identify(text); got != want {
			t.Errorf("identify(%.40q) = %q, want %q", text, got, want)
		}
	}
	dir := t.TempDir()
	if _, err := licensed(dir, "empty", "v1"); err == nil || !strings.Contains(err.Error(), "no licence file") {
		t.Errorf("a module with no licence file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "LICENSE"), []byte("All rights reserved.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := licensed(dir, "odd", "v1"); err == nil || !strings.Contains(err.Error(), "none this tool recognises") {
		t.Errorf("an unrecognised licence: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "COPYING"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := licensed(dir, "two", "v1"); err == nil || !strings.Contains(err.Error(), "more than one licence file") {
		t.Errorf("two licence files: %v", err)
	}
}
