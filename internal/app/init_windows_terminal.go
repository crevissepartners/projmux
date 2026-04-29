package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// wtBinding mirrors one Windows Terminal action+keybinding pair the projmux
// init command guarantees in the user's settings.json. Each entry corresponds
// to one of the 12 projmux app shortcuts; the table is the single source of
// truth in code (docs/keybindings.md mirrors it for users).
type wtBinding struct {
	// ID is the stable settings.json identifier (prefixed with User.projmux).
	// Same value is used in both the actions[] entry and the keybindings[]
	// entry so the two halves stay in sync across re-runs.
	ID string
	// Keys is the canonical key combo (e.g. "alt+1").
	Keys string
	// Input is the literal byte sequence sendInput should write to the
	// pseudo-terminal. Stored as the actual rune sequence (with real escape
	// chars), not the JSON-source `` form.
	Input string
}

// wtIDPrefix marks a settings.json entry as projmux-managed. Anything starting
// with this string in `id` is owned by the merge; anything else is the user's.
const wtIDPrefix = "User.projmux"

// wtDesiredBindings is the canonical list inlined from docs/keybindings.md.
// Update both sites together when CSI-u routing changes.
var wtDesiredBindings = []wtBinding{
	{ID: "User.projmuxSidebar", Keys: "alt+1", Input: "\x1b1"},
	{ID: "User.projmuxSessions", Keys: "alt+2", Input: "\x1b2"},
	{ID: "User.projmuxSwitch", Keys: "alt+3", Input: "\x1b3"},
	{ID: "User.projmuxAIPicker", Keys: "alt+4", Input: "\x1b4"},
	{ID: "User.projmuxSettings", Keys: "alt+5", Input: "\x1b5"},
	{ID: "User.projmuxAISplitRight", Keys: "ctrl+shift+r", Input: "\x02r"},
	{ID: "User.projmuxAISplitDown", Keys: "ctrl+shift+l", Input: "\x02l"},
	{ID: "User.projmuxNewWindow", Keys: "ctrl+n", Input: "\x0e"},
	{ID: "User.projmuxPrevWindow", Keys: "alt+shift+left", Input: "\x1b[1;4D"},
	{ID: "User.projmuxNextWindow", Keys: "alt+shift+right", Input: "\x1b[1;4C"},
	{ID: "User.projmuxRenameWindow", Keys: "ctrl+m", Input: "\x1b[9011u"},
	{ID: "User.projmuxRenamePane", Keys: "ctrl+shift+m", Input: "\x1b[9012u"},
}

// WindowsTerminalAdapter implements TerminalAdapter for Windows Terminal,
// covering both native Windows installs and WSL where the host shell is WT.
type WindowsTerminalAdapter struct {
	// now allows tests to pin the timestamp used for backup file names.
	now func() time.Time
	// userHomeDir defaults to os.UserHomeDir for the WSL fallback path.
	userHomeDir func() (string, error)
	// stat is used to choose between candidate config paths (Store vs
	// unpackaged) and defaults to os.Stat.
	stat func(string) (os.FileInfo, error)
	// writeFile defaults to os.WriteFile.
	writeFile func(name string, data []byte, perm os.FileMode) error
	// mkdirAll defaults to os.MkdirAll for the parent config dir.
	mkdirAll func(path string, perm os.FileMode) error
	// runCmdExe is the WSL interop hook; given args (e.g. ["cmd.exe", "/c",
	// "echo %LOCALAPPDATA%"]) it returns the captured stdout. Tests can swap
	// this for a fake to drive the path resolution logic.
	runCmdExe func(args []string) (string, error)
}

// NewWindowsTerminalAdapter constructs a WT adapter wired to real OS helpers.
// Tests can override the internal hooks afterwards.
func NewWindowsTerminalAdapter() *WindowsTerminalAdapter {
	return &WindowsTerminalAdapter{
		now:         time.Now,
		userHomeDir: os.UserHomeDir,
		stat:        os.Stat,
		writeFile:   os.WriteFile,
		mkdirAll:    os.MkdirAll,
		runCmdExe:   defaultRunCmdExe,
	}
}

// Name implements TerminalAdapter.
func (w *WindowsTerminalAdapter) Name() string { return "windows-terminal" }

// Detect implements TerminalAdapter. WT is identified via:
//   - WT_SESSION (set by Windows Terminal in spawned children, both native
//     and via WSL interop)
//   - TERM_PROGRAM=WindowsTerminal (preview builds and newer stables)
//   - WSL_DISTRO_NAME / WSL_INTEROP (we're in WSL — projmux assumes WT is the
//     host since that's the most common setup; users on a different host can
//     override by passing the terminal name explicitly)
func (w *WindowsTerminalAdapter) Detect(env func(string) string) bool {
	if env == nil {
		return false
	}
	if strings.TrimSpace(env("WT_SESSION")) != "" {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(env("TERM_PROGRAM")), "WindowsTerminal") {
		return true
	}
	if strings.TrimSpace(env("WSL_DISTRO_NAME")) != "" {
		return true
	}
	if strings.TrimSpace(env("WSL_INTEROP")) != "" {
		return true
	}
	return false
}

// isWSL reports whether we appear to be running inside WSL based on the
// environment. WSL governs how ConfigPath resolves the Windows-side
// %LOCALAPPDATA% path.
func isWSL(env func(string) string) bool {
	if env == nil {
		return false
	}
	if strings.TrimSpace(env("WSL_DISTRO_NAME")) != "" {
		return true
	}
	if strings.TrimSpace(env("WSL_INTEROP")) != "" {
		return true
	}
	return false
}

// ConfigPath implements TerminalAdapter. Two branches:
//
//	WSL:    resolve Windows-side %LOCALAPPDATA% via cmd.exe interop, then
//	        translate C:\... into /mnt/c/... ; on interop failure fall back
//	        to /mnt/c/Users/<linux-user>/AppData/Local heuristic.
//	native: probe Store-install path, then unpackaged path; if neither exists
//	        return the Store path so the caller knows where to create one.
//
// In both branches, when both candidates exist the Store path wins because
// that's the modern install location.
func (w *WindowsTerminalAdapter) ConfigPath(env func(string) string) (string, error) {
	if env == nil {
		env = os.Getenv
	}
	if isWSL(env) {
		return w.wslConfigPath(env)
	}
	return w.nativeConfigPath(env)
}

// nativeConfigPath returns the first existing Windows Terminal settings.json
// among the well-known candidates, defaulting to the Store path.
func (w *WindowsTerminalAdapter) nativeConfigPath(env func(string) string) (string, error) {
	localAppData := strings.TrimSpace(env("LOCALAPPDATA"))
	if localAppData == "" {
		// Best-effort recovery: %USERPROFILE%\AppData\Local.
		userProfile := strings.TrimSpace(env("USERPROFILE"))
		if userProfile == "" {
			return "", fmt.Errorf("resolve windows-terminal config: LOCALAPPDATA unset")
		}
		localAppData = filepath.Join(userProfile, "AppData", "Local")
	}
	candidates := wtNativeCandidates(localAppData)
	return w.firstExistingOrDefault(candidates), nil
}

// wtNativeCandidates returns the {Store, unpackaged} settings.json candidates
// rooted at the supplied %LOCALAPPDATA% (or its WSL-translated equivalent).
func wtNativeCandidates(localAppData string) []string {
	return []string{
		filepath.Join(localAppData, "Packages", "Microsoft.WindowsTerminal_8wekyb3d8bbcwe", "LocalState", "settings.json"),
		filepath.Join(localAppData, "Microsoft", "Windows Terminal", "settings.json"),
	}
}

// firstExistingOrDefault stats each candidate and returns the first that
// exists, falling back to candidates[0] when none do (so a fresh install
// still has a sensible default target).
func (w *WindowsTerminalAdapter) firstExistingOrDefault(candidates []string) string {
	statFn := w.stat
	if statFn == nil {
		statFn = os.Stat
	}
	for _, c := range candidates {
		if _, err := statFn(c); err == nil {
			return c
		}
	}
	return candidates[0]
}

// wslConfigPath resolves the Windows Terminal settings.json from inside WSL.
// Preferred path is via cmd.exe interop (gives us the exact %LOCALAPPDATA%
// even when the Windows username differs from the Linux username); on failure
// we fall back to the /mnt/c/Users/<linux-user> heuristic and let the user
// re-run with --config if their Windows username differs.
func (w *WindowsTerminalAdapter) wslConfigPath(env func(string) string) (string, error) {
	runner := w.runCmdExe
	if runner == nil {
		runner = defaultRunCmdExe
	}
	out, err := runner([]string{"cmd.exe", "/c", "echo %LOCALAPPDATA%"})
	if err == nil {
		winPath := strings.TrimSpace(out)
		// cmd.exe occasionally appends \r before the LF we already trimmed.
		winPath = strings.TrimRight(winPath, "\r")
		if winPath != "" && !strings.Contains(winPath, "%") {
			mnt, convErr := winPathToMnt(winPath)
			if convErr == nil {
				return w.firstExistingOrDefault(wtNativeCandidates(mnt)), nil
			}
		}
	}
	// Fallback: /mnt/c/Users/<linux-user>/AppData/Local
	homeFn := w.userHomeDir
	if homeFn == nil {
		homeFn = os.UserHomeDir
	}
	home, herr := homeFn()
	if herr != nil || home == "" {
		return "", fmt.Errorf("resolve windows-terminal config: cmd.exe interop failed (%v) and could not derive home dir (%v)", err, herr)
	}
	user := filepath.Base(home)
	mnt := filepath.Join("/mnt/c/Users", user, "AppData", "Local")
	return w.firstExistingOrDefault(wtNativeCandidates(mnt)), nil
}

// winPathToMnt converts a Windows path like `C:\Users\foo\AppData\Local` into
// its WSL-mounted equivalent `/mnt/c/Users/foo/AppData/Local`. Drive letters
// are lowercased; backslashes become forward slashes.
func winPathToMnt(winPath string) (string, error) {
	if len(winPath) < 2 || winPath[1] != ':' {
		return "", fmt.Errorf("not a drive-rooted windows path: %q", winPath)
	}
	drive := unicode.ToLower(rune(winPath[0]))
	rest := strings.ReplaceAll(winPath[2:], "\\", "/")
	rest = strings.TrimPrefix(rest, "/")
	return "/mnt/" + string(drive) + "/" + rest, nil
}

// defaultRunCmdExe is the production WSL interop hook: it shells out to
// cmd.exe and returns its stdout. Tests replace this with an in-memory fake.
func defaultRunCmdExe(args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("runCmdExe: no command")
	}
	cmd := exec.Command(args[0], args[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w (stderr=%q)", args[0], err, stderr.String())
	}
	return stdout.String(), nil
}

// PlanMerge implements TerminalAdapter. It parses the JSONC settings,
// merges the projmux actions[] / keybindings[] entries idempotently, and
// returns a re-serialised settings document plus the per-binding change
// list. Conflicts on `keys` (the user has the same key bound to a non-
// projmux id) are reported as skip-conflict and left untouched.
func (w *WindowsTerminalAdapter) PlanMerge(currentConfig string, fileExists bool) (MergePlan, error) {
	plan := MergePlan{Original: currentConfig, CreateNew: !fileExists}

	root, err := parseSettings(currentConfig, fileExists)
	if err != nil {
		return MergePlan{}, fmt.Errorf("parse windows-terminal settings.json: %w", err)
	}

	actions := asObjectArray(root["actions"])
	keybindings := asObjectArray(root["keybindings"])

	// Build lookup maps by id (managed entries) and by keys (so conflict
	// detection sees the user's true intent).
	actionByID := map[string]map[string]any{}
	for _, a := range actions {
		if id, ok := a["id"].(string); ok {
			actionByID[id] = a
		}
	}
	keybindingByID := map[string]map[string]any{}
	keybindingByKeys := map[string]map[string]any{}
	for _, kb := range keybindings {
		if id, ok := kb["id"].(string); ok {
			keybindingByID[id] = kb
		}
		if keys, ok := kb["keys"].(string); ok && keys != "" {
			keybindingByKeys[strings.ToLower(keys)] = kb
		}
	}

	var changes []MergeChange
	dirty := false

	for _, want := range wtDesiredBindings {
		// 1) Conflict check: a non-projmux binding already owns these keys.
		if existingKB, ok := keybindingByKeys[strings.ToLower(want.Keys)]; ok {
			existingID, _ := existingKB["id"].(string)
			if !strings.HasPrefix(existingID, wtIDPrefix) {
				existingLabel := existingID
				if existingLabel == "" {
					existingLabel = "<unknown action>"
				}
				changes = append(changes, MergeChange{
					Trigger:  want.Keys,
					Action:   want.ID,
					Existing: existingLabel,
					Kind:     "skip-conflict",
					Reason:   "user mapped keys to " + existingLabel,
				})
				continue
			}
		}

		desiredAction := newProjmuxAction(want)
		desiredKB := newProjmuxKeybinding(want)

		// 2) Action entry: add or update if id not present / drifted.
		actionChanged := false
		if existingAction, ok := actionByID[want.ID]; ok {
			if !sameProjmuxAction(existingAction, desiredAction) {
				// Drift: replace in-place to keep object ordering stable.
				for k := range existingAction {
					delete(existingAction, k)
				}
				for k, v := range desiredAction {
					existingAction[k] = v
				}
				actionChanged = true
				dirty = true
			}
		} else {
			actions = append(actions, desiredAction)
			actionByID[want.ID] = desiredAction
			actionChanged = true
			dirty = true
		}

		// 3) Keybinding entry: same drift check on the {id, keys} pair.
		kbChanged := false
		if existingKB, ok := keybindingByID[want.ID]; ok {
			if !sameProjmuxKeybinding(existingKB, desiredKB) {
				for k := range existingKB {
					delete(existingKB, k)
				}
				for k, v := range desiredKB {
					existingKB[k] = v
				}
				kbChanged = true
				dirty = true
			}
		} else {
			keybindings = append(keybindings, desiredKB)
			keybindingByID[want.ID] = desiredKB
			keybindingByKeys[strings.ToLower(want.Keys)] = desiredKB
			kbChanged = true
			dirty = true
		}

		switch {
		case !actionChanged && !kbChanged:
			changes = append(changes, MergeChange{
				Trigger: want.Keys,
				Action:  want.ID,
				Kind:    "noop",
			})
		default:
			changes = append(changes, MergeChange{
				Trigger: want.Keys,
				Action:  want.ID,
				Kind:    "add",
			})
		}
	}

	// Stable sort changes by trigger so output is deterministic regardless
	// of map iteration order quirks above (the desired list is already in a
	// fixed order, but defence-in-depth keeps tests stable across refactors).
	sort.SliceStable(changes, func(i, j int) bool {
		return changes[i].Trigger < changes[j].Trigger
	})

	plan.Changes = changes

	if !dirty {
		plan.Updated = currentConfig
		return plan, nil
	}

	root["actions"] = actions
	root["keybindings"] = keybindings

	encoded, err := encodeSettings(root)
	if err != nil {
		return MergePlan{}, fmt.Errorf("encode windows-terminal settings.json: %w", err)
	}
	plan.Updated = encoded
	return plan, nil
}

// newProjmuxAction builds the canonical actions[] entry for a binding.
func newProjmuxAction(b wtBinding) map[string]any {
	return map[string]any{
		"command": map[string]any{
			"action": "sendInput",
			"input":  b.Input,
		},
		"id": b.ID,
	}
}

// newProjmuxKeybinding builds the canonical keybindings[] entry for a binding.
func newProjmuxKeybinding(b wtBinding) map[string]any {
	return map[string]any{
		"id":   b.ID,
		"keys": b.Keys,
	}
}

// sameProjmuxAction reports whether two action objects describe the same
// {id, command.action, command.input} triple. Other user-added fields are
// ignored; the merge owns the canonical fields only.
func sameProjmuxAction(have, want map[string]any) bool {
	if have["id"] != want["id"] {
		return false
	}
	hc, _ := have["command"].(map[string]any)
	wc, _ := want["command"].(map[string]any)
	if hc == nil || wc == nil {
		return false
	}
	return hc["action"] == wc["action"] && hc["input"] == wc["input"]
}

// sameProjmuxKeybinding compares the canonical {id, keys} pair.
func sameProjmuxKeybinding(have, want map[string]any) bool {
	return have["id"] == want["id"] && have["keys"] == want["keys"]
}

// asObjectArray normalises an `any` value (the result of unmarshalling a JSON
// array into `any`) into a slice of map[string]any. Non-array or non-object
// elements are dropped — Windows Terminal would ignore them anyway, so this
// keeps the merge tolerant of malformed user content.
func asObjectArray(v any) []map[string]any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// parseSettings reads the JSONC settings document into a generic object map.
// Comments and trailing commas are stripped before passing to encoding/json
// so we can stay on the standard library. An empty / missing file produces
// an empty map so the merge can populate it from scratch.
func parseSettings(raw string, fileExists bool) (map[string]any, error) {
	if !fileExists || strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	cleaned := stripJSONC(raw)
	dec := json.NewDecoder(strings.NewReader(cleaned))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	root, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("settings.json root must be an object, got %T", v)
	}
	return root, nil
}

// encodeSettings serialises the merged document back to disk. We re-emit as
// standard JSON with two-space indent (matching Windows Terminal's default
// formatting). User comments cannot survive a strip-then-reparse round-trip,
// so the dispatcher creates a timestamped backup before write.
func encodeSettings(root map[string]any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "    ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// stripJSONC removes // line comments, /* block comments */, and trailing
// commas before object/array closers, leaving a string that encoding/json
// can parse. Comments inside string literals are preserved because the
// scanner tracks the in-string state.
//
// The implementation walks the string byte-by-byte. We avoid pulling in a
// third-party JSONC parser to keep the module dependency-free; the cost is
// that user comments are not round-tripped (the apply step keeps a backup
// to compensate).
func stripJSONC(raw string) string {
	var out bytes.Buffer
	out.Grow(len(raw))

	inString := false
	escape := false
	i := 0
	for i < len(raw) {
		c := raw[i]
		if inString {
			out.WriteByte(c)
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				inString = false
			}
			i++
			continue
		}
		// Line comment
		if c == '/' && i+1 < len(raw) && raw[i+1] == '/' {
			i += 2
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			continue
		}
		// Block comment
		if c == '/' && i+1 < len(raw) && raw[i+1] == '*' {
			i += 2
			for i+1 < len(raw) && !(raw[i] == '*' && raw[i+1] == '/') {
				i++
			}
			if i+1 < len(raw) {
				i += 2
			} else {
				i = len(raw)
			}
			continue
		}
		if c == '"' {
			inString = true
			out.WriteByte(c)
			i++
			continue
		}
		out.WriteByte(c)
		i++
	}
	return stripTrailingCommas(out.String())
}

// stripTrailingCommas removes commas that immediately precede a `}` or `]`
// (modulo whitespace). It assumes JSONC comments have already been stripped
// and tracks the in-string state to leave string contents alone.
func stripTrailingCommas(s string) string {
	out := make([]byte, 0, len(s))
	inString := false
	escape := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			out = append(out, c)
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r') {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				// Skip the comma; preserve following whitespace.
				continue
			}
		}
		out = append(out, c)
	}
	return string(out)
}

// ApplyMerge implements TerminalAdapter. It backs up the prior settings.json
// to <path>.bak.<YYYYMMDD-HHMMSS> when one existed and writes the merged
// document atomically (via the standard write-then-rename of os.WriteFile is
// good enough on the host filesystems we target; drvfs (/mnt/c) writes use
// the same path and surface umask-related EACCES errors with the original
// message intact).
func (w *WindowsTerminalAdapter) ApplyMerge(plan MergePlan) error {
	if plan.ConfigPath == "" {
		return fmt.Errorf("apply windows-terminal merge: plan has no ConfigPath")
	}
	if !plan.HasEffect() {
		return nil
	}

	dir := filepath.Dir(plan.ConfigPath)
	mkdir := w.mkdirAll
	if mkdir == nil {
		mkdir = os.MkdirAll
	}
	if err := mkdir(dir, 0o755); err != nil {
		return fmt.Errorf("create windows-terminal config dir %s: %w", dir, err)
	}

	write := w.writeFile
	if write == nil {
		write = os.WriteFile
	}

	if !plan.CreateNew {
		nowFn := w.now
		if nowFn == nil {
			nowFn = time.Now
		}
		stamp := nowFn().Format("20060102-150405")
		backup := plan.ConfigPath + ".bak." + stamp
		if err := write(backup, []byte(plan.Original), 0o644); err != nil {
			return fmt.Errorf("write windows-terminal settings backup %s: %w", backup, err)
		}
	}

	if err := write(plan.ConfigPath, []byte(plan.Updated), 0o644); err != nil {
		return fmt.Errorf("write windows-terminal settings %s: %w", plan.ConfigPath, err)
	}
	return nil
}
