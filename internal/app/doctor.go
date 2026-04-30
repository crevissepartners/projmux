package app

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type doctorCommand struct {
	lookPath       func(string) (string, error)
	goos           func() string
	getenv         func(string) string
	commandVersion func(name string) string
}

func newDoctorCommand() *doctorCommand {
	c := &doctorCommand{
		lookPath: exec.LookPath,
		goos:     func() string { return runtime.GOOS },
		getenv:   os.Getenv,
	}
	c.commandVersion = func(name string) string {
		return defaultCommandVersion(name)
	}
	return c
}

type doctorDepCategory string

const (
	doctorCategoryCore     doctorDepCategory = "core"
	doctorCategoryWorkflow doctorDepCategory = "workflow"
	doctorCategoryOptional doctorDepCategory = "optional"
)

type doctorDep struct {
	Name     string
	Required bool
	Category doctorDepCategory
	// SkipOnWindows marks deps that are not applicable on windows hosts
	// (e.g. POSIX-only stty).
	SkipOnWindows bool
	// PackageNames maps a package-manager key (apt, brew, pacman, dnf,
	// zypper, apk, scoop) to the install package name when it differs from
	// the binary name. Missing entries default to the binary name.
	PackageNames map[string]string
	// FallbackHint is a non-OS-specific extra suggestion appended after the
	// detected install command (e.g. `go install` for fzf).
	FallbackHint string
	// OptionalNote is the human-readable explanation rendered for optional
	// deps regardless of presence.
	OptionalNote string
	// MinVersion is the inclusive minimum semver-ish version required.
	// Empty means no version check is performed.
	MinVersion string
}

type doctorStatus string

const (
	doctorStatusOK      doctorStatus = "ok"
	doctorStatusMissing doctorStatus = "missing"
	doctorStatusStale   doctorStatus = "stale"
	doctorStatusHint    doctorStatus = "hint"
	doctorStatusSkip    doctorStatus = "skip"
)

type doctorResult struct {
	Name     string       `json:"name"`
	Required bool         `json:"required"`
	Status   doctorStatus `json:"status"`
	Version  string       `json:"version,omitempty"`
	Hint     string       `json:"hint,omitempty"`
	Install  string       `json:"install,omitempty"`
}

func doctorDeps() []doctorDep {
	return []doctorDep{
		{Name: "tmux", Required: true, Category: doctorCategoryCore, MinVersion: "3.4"},
		{
			Name:         "fzf",
			Required:     true,
			Category:     doctorCategoryCore,
			FallbackHint: "or: go install github.com/junegunn/fzf@latest",
			MinVersion:   "0.55",
		},
		{Name: "git", Required: true, Category: doctorCategoryWorkflow},
		{Name: "stty", Required: true, Category: doctorCategoryWorkflow, SkipOnWindows: true},
		{
			Name:         "kubectl",
			Required:     false,
			Category:     doctorCategoryOptional,
			OptionalNote: "optional; install if you use the kubectl switcher",
		},
	}
}

// Run executes the projmux doctor diagnostics flow.
func (c *doctorCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON instead of the text report")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("doctor does not accept positional arguments")
	}

	results := c.evaluate()

	if *jsonOut {
		return writeDoctorJSON(stdout, results)
	}

	if err := writeDoctorText(stdout, results); err != nil {
		return err
	}

	for _, r := range results {
		if r.Required && (r.Status == doctorStatusMissing || r.Status == doctorStatusStale) {
			return fmt.Errorf("missing required dependencies; see report above")
		}
	}
	return nil
}

func (c *doctorCommand) evaluate() []doctorResult {
	host := c.goos()
	deps := doctorDeps()
	out := make([]doctorResult, 0, len(deps))
	for _, dep := range deps {
		out = append(out, c.evaluateDep(dep, host))
	}
	return out
}

func (c *doctorCommand) evaluateDep(dep doctorDep, host string) doctorResult {
	res := doctorResult{
		Name:     dep.Name,
		Required: dep.Required,
	}

	if dep.SkipOnWindows && host == "windows" {
		res.Status = doctorStatusSkip
		res.Hint = "windows host"
		return res
	}

	if _, err := c.lookPath(dep.Name); err == nil {
		res.Status = doctorStatusOK
		if c.commandVersion != nil {
			res.Version = c.commandVersion(dep.Name)
		}
		if dep.MinVersion != "" {
			atLeast, parsed := versionAtLeast(res.Version, dep.MinVersion)
			if parsed && !atLeast {
				res.Status = doctorStatusStale
				res.Hint = fmt.Sprintf("minimum %s; found %s", dep.MinVersion, res.Version)
				res.Install = detectInstallHint(dep, host, c.lookPath)
			}
		}
		return res
	}

	install := detectInstallHint(dep, host, c.lookPath)

	if !dep.Required {
		res.Status = doctorStatusHint
		res.Hint = dep.OptionalNote
		res.Install = install
		return res
	}

	res.Status = doctorStatusMissing
	res.Install = install
	return res
}

func writeDoctorText(w io.Writer, results []doctorResult) error {
	var buf bytes.Buffer
	buf.WriteString("projmux doctor\n")

	var ok, missing, stale, skipped, hints int
	for _, r := range results {
		tag := fmt.Sprintf("[%s]", r.Status)
		// Why: pad tag column to fit "[missing]" so subsequent columns line up.
		fmt.Fprintf(&buf, "  %-10s%-10s", tag, r.Name)

		switch r.Status {
		case doctorStatusOK:
			ok++
			if r.Version != "" {
				buf.WriteString(r.Version)
			}
		case doctorStatusMissing:
			missing++
			buf.WriteString("- install: ")
			if r.Install != "" {
				buf.WriteString(r.Install)
			} else {
				buf.WriteString("see https://github.com/crevissepartners/projmux for guidance")
			}
		case doctorStatusStale:
			stale++
			buf.WriteString("- ")
			if r.Hint != "" {
				buf.WriteString(r.Hint)
			}
			if r.Install != "" {
				buf.WriteString("; install: ")
				buf.WriteString(r.Install)
			}
		case doctorStatusHint:
			hints++
			buf.WriteString("- ")
			if r.Hint != "" {
				buf.WriteString(r.Hint)
			}
			if r.Install != "" {
				buf.WriteString("; install: ")
				buf.WriteString(r.Install)
			}
		case doctorStatusSkip:
			skipped++
			buf.WriteString("- ")
			if r.Hint != "" {
				buf.WriteString(r.Hint)
			}
		}
		buf.WriteString("\n")
	}

	fmt.Fprintf(&buf, "\n%d ok, %d missing, %d stale, %d skipped, %d hint.\n", ok, missing, stale, skipped, hints)
	_, err := w.Write(buf.Bytes())
	return err
}

func writeDoctorJSON(w io.Writer, results []doctorResult) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}

func detectInstallHint(dep doctorDep, host string, lookPath func(string) (string, error)) string {
	if lookPath == nil {
		return ""
	}
	pkg := func(key string) string {
		if name, ok := dep.PackageNames[key]; ok && name != "" {
			return name
		}
		return dep.Name
	}

	var primary string
	switch host {
	case "darwin":
		if _, err := lookPath("brew"); err == nil {
			primary = "brew install " + pkg("brew")
		}
	case "linux":
		switch {
		case hasOnPath(lookPath, "apt-get"):
			primary = "sudo apt-get install -y " + pkg("apt")
		case hasOnPath(lookPath, "pacman"):
			primary = "sudo pacman -S " + pkg("pacman")
		case hasOnPath(lookPath, "dnf"):
			primary = "sudo dnf install " + pkg("dnf")
		case hasOnPath(lookPath, "zypper"):
			primary = "sudo zypper install " + pkg("zypper")
		case hasOnPath(lookPath, "apk"):
			primary = "sudo apk add " + pkg("apk")
		}
	case "windows":
		primary = "scoop install " + pkg("scoop")
	}

	if dep.FallbackHint != "" {
		if primary == "" {
			return dep.FallbackHint
		}
		return primary + " (" + dep.FallbackHint + ")"
	}
	return primary
}

func hasOnPath(lookPath func(string) (string, error), name string) bool {
	if lookPath == nil {
		return false
	}
	_, err := lookPath(name)
	return err == nil
}

// versionProbeArgs maps a binary name to the argv that prints its version.
// Most tools accept --version, but tmux uses -V and kubectl uses
// `version --client` (its --version flag is unsupported).
var versionProbeArgs = map[string][]string{
	"tmux":    {"-V"},
	"kubectl": {"version", "--client"},
}

// doctorVersionPattern matches the first version-looking token in a string,
// tolerating a trailing single-letter suffix (tmux uses 3.4a/3.4b convention).
var doctorVersionPattern = regexp.MustCompile(`\d+(\.\d+){0,2}[a-z]?`)

// parseDoctorVersion extracts a (major, minor, patch) tuple from a tool's
// version-output line. Missing components default to 0. Trailing letter
// suffixes on tmux (e.g. "3.4a") are tolerated. Returns ok=false when no
// version-looking token is found.
func parseDoctorVersion(raw string) (major, minor, patch int, ok bool) {
	tok := doctorVersionPattern.FindString(raw)
	if tok == "" {
		return 0, 0, 0, false
	}
	if last := tok[len(tok)-1]; last >= 'a' && last <= 'z' {
		tok = tok[:len(tok)-1]
	}
	if tok == "" {
		return 0, 0, 0, false
	}
	parts := strings.Split(tok, ".")
	out := [3]int{}
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return 0, 0, 0, false
		}
		out[i] = n
	}
	return out[0], out[1], out[2], true
}

// versionAtLeast reports whether got >= want using lexicographic
// (major, minor, patch) comparison. Both inputs are parsed via
// parseDoctorVersion. If parsing fails on either side, returns true,
// false (i.e. cannot determine — treat as ok rather than failing
// loudly on parse glitches).
func versionAtLeast(got, want string) (atLeast bool, parsed bool) {
	gM, gm, gp, gok := parseDoctorVersion(got)
	wM, wm, wp, wok := parseDoctorVersion(want)
	if !gok || !wok {
		return true, false
	}
	g := [3]int{gM, gm, gp}
	w := [3]int{wM, wm, wp}
	for i := 0; i < 3; i++ {
		if g[i] != w[i] {
			return g[i] > w[i], true
		}
	}
	return true, true
}

func defaultCommandVersion(name string) string {
	args := versionProbeArgs[name]
	if len(args) == 0 {
		args = []string{"--version"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	first := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	return first
}
