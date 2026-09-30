package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
)

// command is the package whose dependencies are what the binary links.
const command = "./cmd/projmux"

// platforms are the supported build targets (AGENTS.md, Compatibility). A
// module any of them links is listed, so one file serves every binary.
var platforms = [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"}}

// part is one thing the binary is made of, as the notices name it.
type part struct {
	// name is a module path, "Go" for the runtime and standard library, or
	// "projmux" for this program.
	name string
	// version is a module's version, Go's version, or "" for projmux, whose
	// version is the binary's own.
	version string
	// license is the SPDX identifier the licence text was recognised as.
	license string
	// text is the licence file, and notice the NOTICE file when there is one.
	text   string
	notice string
}

func (p part) heading() string {
	if p.version == "" {
		return p.name
	}
	return p.name + " " + p.version
}

// collect reads the parts from the repository at root: projmux's own
// licence, Go's from GOROOT at the version go.mod's toolchain line names,
// and each module `go list -deps` finds under the command on any supported
// platform, with the licence file in its module directory. Test and tool
// dependencies are not dependencies of the command, so they are not listed.
//
// The result is a function of go.mod, go.sum and the files they pin: a
// module's directory in the module cache is the content go.sum verified, and
// GOROOT is the toolchain go.mod names. No time, host or path is recorded.
func collect(root string) ([]part, error) {
	own, err := licensed(root, "projmux", "")
	if err != nil {
		return nil, err
	}
	version, err := toolchain(root)
	if err != nil {
		return nil, err
	}
	goroot, err := goOutput(root, nil, "env", "GOROOT")
	if err != nil {
		return nil, err
	}
	runtime, err := licensed(strings.TrimSpace(goroot), "Go", version)
	if err != nil {
		return nil, err
	}
	parts := []part{own, runtime}

	main, err := goOutput(root, nil, "list", "-m")
	if err != nil {
		return nil, err
	}
	modules := map[string]bool{}
	for _, platform := range platforms {
		env := []string{"GOOS=" + platform[0], "GOARCH=" + platform[1], "CGO_ENABLED=0"}
		out, err := goOutput(root, env, "list", "-deps", "-f", "{{with .Module}}{{.Path}}{{end}}", command)
		if err != nil {
			return nil, err
		}
		for path := range strings.FieldsSeq(out) {
			if path != strings.TrimSpace(main) {
				modules[path] = true
			}
		}
	}
	paths := make([]string, 0, len(modules))
	for path := range modules {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		out, err := goOutput(root, nil, "list", "-m", "-json", path)
		if err != nil {
			return nil, err
		}
		var module struct{ Path, Version, Dir string }
		if err := json.Unmarshal([]byte(out), &module); err != nil {
			return nil, fmt.Errorf("go list -m -json %s: %w", path, err)
		}
		if module.Dir == "" {
			return nil, fmt.Errorf("module %s is not in the module cache; run `go mod download`", path)
		}
		listed, err := licensed(module.Dir, module.Path, module.Version)
		if err != nil {
			return nil, err
		}
		parts = append(parts, listed)
	}
	return parts, nil
}

// toolchain is the Go version go.mod names: its toolchain line, else its go
// line. It is the version the binary is built with wherever it is built,
// since an older go command fetches it.
func toolchain(root string) (string, error) {
	path := filepath.Join(root, "go.mod")
	data, err := os.ReadFile(path) // #nosec G304 -- go.mod of the repository the tool is run in
	if err != nil {
		return "", err
	}
	file, err := modfile.Parse(path, data, nil)
	if err != nil {
		return "", err
	}
	switch {
	case file.Toolchain != nil:
		return strings.TrimPrefix(file.Toolchain.Name, "go"), nil
	case file.Go != nil:
		return file.Go.Version, nil
	}
	return "", errors.New("go.mod names no Go version")
}

// goOutput runs the go command in the repository with extra environment.
func goOutput(root string, env []string, args ...string) (string, error) {
	cmd := exec.Command("go", args...) // #nosec G204 -- the go command, with arguments this file spells out
	cmd.Dir = root
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

var (
	licenseFile = regexp.MustCompile(`(?i)^(licen[sc]e|copying)(\.(txt|md))?$`)
	noticeFile  = regexp.MustCompile(`(?i)^notice(\.(txt|md))?$`)
)

// licensed reads the licence of what is in dir: exactly one licence file,
// whose text has to be one this tool recognises, and a NOTICE when there is
// one. Anything else is an error, for a person to look at.
func licensed(dir, name, version string) (part, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return part{}, fmt.Errorf("%s: %w", name, err)
	}
	p := part{name: name, version: version}
	read := func(file string) (string, error) {
		data, err := os.ReadFile(filepath.Join(dir, file)) // #nosec G304 -- a licence file of a module go.sum pins, of GOROOT or of this repository
		return normalize(string(data)), err
	}
	for _, entry := range entries {
		switch {
		case entry.IsDir():
		case licenseFile.MatchString(entry.Name()):
			if p.text != "" {
				return part{}, fmt.Errorf("%s has more than one licence file", name)
			}
			if p.text, err = read(entry.Name()); err != nil {
				return part{}, err
			}
		case noticeFile.MatchString(entry.Name()):
			if p.notice, err = read(entry.Name()); err != nil {
				return part{}, err
			}
		}
	}
	if p.text == "" {
		return part{}, fmt.Errorf("%s has no licence file in %s", name, filepath.Base(dir))
	}
	if p.license = identify(p.text); p.license == "" {
		return part{}, fmt.Errorf("%s: its licence text is none this tool recognises; add it to identify", name)
	}
	return p, nil
}

// normalize gives a text one line ending and no blank lines around it.
func normalize(text string) string {
	return strings.Trim(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
}

// identify names a licence by the words only it has. A Go module carries no
// licence identifier, so the text is the only evidence; a text that is none
// of these is not guessed at.
func identify(text string) string {
	flat := strings.Join(strings.Fields(text), " ")
	has := func(words string) bool { return strings.Contains(flat, words) }
	switch {
	case has("Apache License") && has("Version 2.0, January 2004"):
		return "Apache-2.0"
	case has("Redistribution and use in source and binary forms") && has("Neither the name of"):
		return "BSD-3-Clause"
	case has("Permission is hereby granted, free of charge") && has(`THE SOFTWARE IS PROVIDED "AS IS"`):
		return "MIT"
	}
	return ""
}

// rule is the line around a part's heading, which section reads back.
var rule = strings.Repeat("=", 80)

// render writes the notices: a sentence on what they cover, then a section
// for each part, in the order given.
func render(w io.Writer, parts []part) error {
	var out strings.Builder
	out.WriteString("The projmux program is projmux itself, the Go runtime and standard library\n")
	out.WriteString("(\"Go\", at the version the program is built with), and the Go modules below.\n")
	out.WriteString("Each is listed with its version, its license, and its license text.\n")
	for _, p := range parts {
		fmt.Fprintf(&out, "\n%s\n%s\nLicense: %s\n%s\n\n%s\n", rule, p.heading(), p.license, rule, p.text)
		if p.notice != "" {
			fmt.Fprintf(&out, "\nNOTICE\n\n%s\n", p.notice)
		}
	}
	_, err := io.WriteString(w, out.String())
	return err
}

var section = regexp.MustCompile(`(?m)^={80}\n(.+)\nLicense: (.+)\n={80}\n`)

// listed are the headings and licences of the sections a notices text has,
// as "heading (license)".
func listed(text string) []string {
	var out []string
	for _, match := range section.FindAllStringSubmatch(text, -1) {
		out = append(out, match[1]+" ("+match[2]+")")
	}
	return out
}

// problems says how a notices text disagrees with the parts the binary is
// made of: a part with no section, a section for something that is not a
// part (or another version, or another licence), a section twice, or a text
// that is not what these parts render to. Empty when they agree.
func problems(text string, parts []part) []string {
	var want []string
	for _, p := range parts {
		want = append(want, p.heading()+" ("+p.license+")")
	}
	have := listed(text)
	var out []string
	for _, name := range want {
		if !slices.Contains(have, name) {
			out = append(out, "missing: "+name+" is in the binary and has no notice")
		}
	}
	seen := map[string]bool{}
	for _, name := range have {
		if !slices.Contains(want, name) {
			out = append(out, "extra: "+name+" has a notice and is not in the binary")
		}
		if seen[name] {
			out = append(out, "twice: "+name)
		}
		seen[name] = true
	}
	if len(out) == 0 {
		var fresh bytes.Buffer
		if err := render(&fresh, parts); err != nil || fresh.String() != text {
			out = append(out, "stale: the text is not what these parts render to")
		}
	}
	return out
}
