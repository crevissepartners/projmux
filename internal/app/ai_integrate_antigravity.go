package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	antigravityHooksRelativePath = ".gemini/config/hooks.json"
	antigravityManagedHookName   = "projmux"
	antigravityManagedMarker     = "projmux-managed:antigravity-hooks:v1"

	antigravityCanonicalIngestPath = " internal agent-hook ingest antigravity-hook"

	// Legacy statusLine ownership: older projmux releases installed a
	// statusLine bridge. These remain only so upgrades can remove it.
	antigravitySettingsRelativePath   = ".gemini/antigravity-cli/settings.json"
	antigravityLegacyStatusLineKey    = "statusLine"
	antigravityLegacyStatusLineMarker = "projmux-managed:antigravity-statusline:v1"
)

var (
	antigravityLegacyIngestPath = legacyAIIngestArgs("antigravity-hook")
	antigravityManagedEvents    = []string{
		"PreInvocation",
		"PostInvocation",
		"PostToolUse",
		"Stop",
	}
)

type antigravityHookPlan struct {
	path       string
	current    string
	next       string
	action     string
	executable string
	changed    bool
	existed    bool
	conflict   string
}

type antigravityNamedHook struct {
	PreInvocation  []antigravityCommandHandler `json:"PreInvocation"`
	PostInvocation []antigravityCommandHandler `json:"PostInvocation"`
	PostToolUse    []antigravityToolHookGroup  `json:"PostToolUse"`
	Stop           []antigravityCommandHandler `json:"Stop"`
}

type antigravityToolHookGroup struct {
	Matcher string                      `json:"matcher"`
	Hooks   []antigravityCommandHandler `json:"hooks"`
}

type antigravityCommandHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type jsonObjectMember struct {
	key        string
	memberFrom int
	valueFrom  int
	valueTo    int
}

func (c *aiCommand) runIntegrateAntigravity(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ai integrate antigravity", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "print planned Antigravity hook changes without writing")
	remove := fs.Bool("remove", false, "remove the projmux-managed Antigravity hook entry")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printAIUsage(stderr)
		return errors.New("ai integrate antigravity does not accept positional arguments")
	}

	hookPlan, err := c.planAntigravityHookIntegration(*remove)
	if err != nil {
		return err
	}
	// Only --remove looks at settings.json, and only to strip a statusLine
	// entry an older projmux wrote. Install never writes or creates it.
	var legacyPlan antigravityLegacyStatusLinePlan
	if *remove {
		legacyPlan = c.planAntigravityLegacyStatusLineRemoval()
	}
	if *dryRun {
		if err := printAntigravityHookDryRun(stdout, hookPlan); err != nil {
			return err
		}
		if legacyPlan.changed {
			_, err := fmt.Fprintf(stdout, "would remove legacy projmux Antigravity statusLine from %s\n", legacyPlan.path)
			return err
		}
		return nil
	}
	if hookPlan.conflict != "" {
		return errors.New(hookPlan.conflict + "; run `projmux agent integrate antigravity --dry-run` to inspect without writing")
	}
	// Preflight both files before either write so a known permission failure
	// cannot leave only half of the removal applied.
	for _, target := range []struct {
		path    string
		changed bool
		label   string
	}{
		{hookPlan.path, hookPlan.changed, "hooks"},
		{legacyPlan.path, legacyPlan.changed, "settings"},
	} {
		if target.changed {
			if err := preflightManagedIngestWrite(target.path); err != nil {
				return fmt.Errorf("write Antigravity %s %s: %w", target.label, target.path, err)
			}
		}
	}
	if hookPlan.changed {
		if err := c.writeAntigravityJSON(hookPlan.path, []byte(hookPlan.next), 0o644, "hooks"); err != nil {
			return err
		}
	}
	if legacyPlan.changed {
		if err := c.writeAntigravityJSON(legacyPlan.path, []byte(legacyPlan.next), legacyPlan.mode, "settings"); err != nil {
			rollbackErr := c.restoreAntigravityPlan(legacyPlan.path, legacyPlan.current, legacyPlan.mode, true)
			if hookPlan.changed {
				if hookRollbackErr := c.restoreAntigravityPlan(hookPlan.path, hookPlan.current, 0o644, hookPlan.existed); hookRollbackErr != nil && rollbackErr == nil {
					rollbackErr = hookRollbackErr
				}
			}
			if rollbackErr != nil {
				return fmt.Errorf("%w (rollback Antigravity integration failed: %v)", err, rollbackErr)
			}
			return err
		}
	}
	if _, err := fmt.Fprintln(stdout, hookPlan.action); err != nil {
		return err
	}
	if legacyPlan.changed {
		_, err = fmt.Fprintln(stdout, "removed legacy projmux Antigravity statusLine from "+legacyPlan.path)
	}
	return err
}

// antigravityLegacyStatusLinePlan describes the removal of a statusLine entry
// that an older projmux installed into Antigravity settings.json. projmux no
// longer installs, judges, or ingests the statusLine; this plan exists only so
// upgrades can take the marker-owned entry back out.
type antigravityLegacyStatusLinePlan struct {
	path    string
	current string
	next    string
	mode    os.FileMode
	changed bool
}

// planAntigravityLegacyStatusLineRemoval never fails: a missing, unreadable,
// symlinked, or malformed settings.json, and any statusLine value without the
// legacy marker, all yield an unchanged plan so the statusLine can never block
// install. Only an object value whose command carries the legacy marker is
// removed, and only that one member.
func (c *aiCommand) planAntigravityLegacyStatusLineRemoval() antigravityLegacyStatusLinePlan {
	home, err := c.homeDir()
	if err != nil {
		return antigravityLegacyStatusLinePlan{}
	}
	path := filepath.Join(home, antigravitySettingsRelativePath)
	plan := antigravityLegacyStatusLinePlan{path: path, mode: 0o600}
	current, exists, err := c.readAntigravityJSON(home, path, "settings")
	if err != nil || !exists {
		return plan
	}
	plan.current = current
	plan.next = current
	members, _, err := scanJSONObject(current)
	if err != nil {
		return plan
	}
	for i, member := range members {
		if member.key != antigravityLegacyStatusLineKey {
			continue
		}
		if !isLegacyProjmuxAntigravityStatusLine(current[member.valueFrom:member.valueTo]) {
			return plan
		}
		if info, err := os.Stat(path); err == nil {
			plan.mode = info.Mode().Perm()
		}
		plan.next = removeJSONObjectMember(current, members, i)
		plan.changed = plan.next != plan.current
		return plan
	}
	return plan
}

func isLegacyProjmuxAntigravityStatusLine(value string) bool {
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &decoded); err != nil || decoded == nil {
		return false
	}
	var command string
	if err := json.Unmarshal(decoded["command"], &command); err != nil {
		return false
	}
	return strings.Contains(command, antigravityLegacyStatusLineMarker)
}

func (c *aiCommand) planAntigravityHookIntegration(remove bool) (antigravityHookPlan, error) {
	home, err := c.homeDir()
	if err != nil {
		return antigravityHookPlan{}, fmt.Errorf("resolve home directory: %w", err)
	}
	path := filepath.Join(home, antigravityHooksRelativePath)
	current, exists, err := c.readAntigravityHooks(home, path)
	if err != nil {
		return antigravityHookPlan{}, err
	}
	plan := antigravityHookPlan{path: path, current: current, next: current, existed: exists}
	if !exists {
		if remove {
			plan.action = "no changes: projmux-managed Antigravity hooks are not present in " + path
			return plan, nil
		}
		current = "{}\n"
		plan.current = ""
	}

	members, closeAt, err := scanJSONObject(current)
	if err != nil {
		return antigravityHookPlan{}, fmt.Errorf("parse Antigravity hooks %s: %w", path, err)
	}
	managedIndex := -1
	for i, member := range members {
		value := current[member.valueFrom:member.valueTo]
		if member.key == antigravityManagedHookName {
			managedIndex = i
			if !jsonHasCommandContaining(value, antigravityManagedMarker) {
				plan.conflict = fmt.Sprintf("Antigravity hooks %s already contains an unmanaged named entry %q", path, antigravityManagedHookName)
				plan.action = "would refuse to modify unmanaged Antigravity hook entry"
				return plan, nil
			}
			continue
		}
		if jsonHasCommandContaining(value, antigravityManagedMarker) || jsonHasAntigravityIngestCommand(value) {
			plan.conflict = fmt.Sprintf("Antigravity hooks %s contains a projmux Antigravity ingest command outside the managed %q entry (found in %q)", path, antigravityManagedHookName, member.key)
			plan.action = "would refuse to install duplicate Antigravity hook wiring"
			return plan, nil
		}
	}

	if remove {
		if managedIndex < 0 {
			plan.action = "no changes: projmux-managed Antigravity hooks are not present in " + path
			return plan, nil
		}
		plan.next = removeJSONObjectMember(current, members, managedIndex)
		plan.changed = plan.next != plan.current
		plan.action = "removed projmux-managed Antigravity hooks from " + path
		return plan, nil
	}

	executable, err := c.persistentExecutablePath()
	if err != nil {
		return antigravityHookPlan{}, fmt.Errorf("resolve stable absolute projmux executable for Antigravity hooks: %w", err)
	}
	plan.executable = executable
	entry, err := encodeAntigravityManagedHook(executable)
	if err != nil {
		return antigravityHookPlan{}, err
	}
	if managedIndex >= 0 {
		member := members[managedIndex]
		entry = indentJSONValue(entry, jsonMemberIndent(current, member.memberFrom))
		plan.next = current[:member.valueFrom] + entry + current[member.valueTo:]
	} else {
		plan.next = appendJSONObjectMember(current, members, closeAt, antigravityManagedHookName, entry)
	}
	plan.changed = plan.next != plan.current
	if plan.changed {
		plan.action = "configured Antigravity hooks in " + path
	} else {
		plan.action = "no changes: Antigravity hooks are already configured in " + path
	}
	return plan, nil
}

func jsonHasCommandContaining(value, needle string) bool {
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return false
	}
	return walkJSONCommands(decoded, needle)
}

func jsonHasAntigravityIngestCommand(value string) bool {
	return jsonHasCommandContaining(value, antigravityLegacyIngestPath) ||
		jsonHasCommandContaining(value, antigravityCanonicalIngestPath)
}

func walkJSONCommands(value any, needle string) bool {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "command" {
				if command, ok := child.(string); ok && strings.Contains(command, needle) {
					return true
				}
			}
			if walkJSONCommands(child, needle) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if walkJSONCommands(child, needle) {
				return true
			}
		}
	}
	return false
}

func (c *aiCommand) persistentExecutablePath() (string, error) {
	resolve := c.executable
	if resolve == nil {
		resolve = resolveExecutablePath
	}
	path, err := resolve()
	if err != nil {
		return "", err
	}
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("resolved executable path is not absolute: %s", path)
	}
	return path, nil
}

func (c *aiCommand) readAntigravityHooks(home, path string) (string, bool, error) {
	return c.readAntigravityJSON(home, path, "hooks")
}

func (c *aiCommand) readAntigravityJSON(home, path, label string) (string, bool, error) {
	if err := rejectSymlinkPath(home, path); err != nil {
		return "", false, fmt.Errorf("inspect Antigravity %s %s: %w", label, path, err)
	}
	readFile := c.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(path)
	if err == nil {
		return string(data), true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	return "", false, fmt.Errorf("read Antigravity %s %s: %w", label, path, err)
}

func rejectSymlinkPath(home, path string) error {
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("hooks path is outside the resolved home directory")
	}
	current := home
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink path component %s", current)
		}
	}
	return nil
}

func (c *aiCommand) writeAntigravityJSON(path string, data []byte, mode os.FileMode, label string) error {
	mkdirAll := c.mkdirAll
	if mkdirAll == nil {
		mkdirAll = os.MkdirAll
	}
	if err := mkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create Antigravity %s directory %s: %w", label, filepath.Dir(path), err)
	}
	writeFile := c.writeFile
	if writeFile == nil {
		writeFile = os.WriteFile
	}
	if err := writeFile(path, data, mode); err != nil {
		return fmt.Errorf("write Antigravity %s %s: %w", label, path, err)
	}
	return nil
}

func (c *aiCommand) restoreAntigravityPlan(path, current string, mode os.FileMode, existed bool) error {
	if !existed {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		home, err := c.homeDir()
		if err != nil {
			return err
		}
		for dir := filepath.Dir(path); dir != home && dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
			if err := os.Remove(dir); err != nil {
				if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrExist) || errors.Is(err, os.ErrPermission) {
					break
				}
				return err
			}
		}
		return nil
	}
	writeFile := c.writeFile
	if writeFile == nil {
		writeFile = os.WriteFile
	}
	return writeFile(path, []byte(current), mode)
}

func encodeAntigravityManagedHook(executable string) (string, error) {
	handler := func(event string) antigravityCommandHandler {
		return antigravityCommandHandler{
			Type:    "command",
			Command: antigravityManagedCommand(executable, event),
			Timeout: 30,
		}
	}
	entry := antigravityNamedHook{
		PreInvocation:  []antigravityCommandHandler{handler("PreInvocation")},
		PostInvocation: []antigravityCommandHandler{handler("PostInvocation")},
		PostToolUse: []antigravityToolHookGroup{{
			Matcher: "*",
			Hooks:   []antigravityCommandHandler{handler("PostToolUse")},
		}},
		Stop: []antigravityCommandHandler{handler("Stop")},
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode projmux-managed Antigravity hook: %w", err)
	}
	return string(data), nil
}

func antigravityManagedCommand(executable, event string) string {
	fallback := "{}"
	if event == "Stop" {
		fallback = `{"decision":"stop"}`
	}
	return shellQuote(executable) + antigravityCanonicalIngestPath + " --event " + event + aiHookPaneArgument +
		" || printf '%s\\n' '" + fallback + "' # " + antigravityManagedMarker
}

func printAntigravityHookDryRun(stdout io.Writer, plan antigravityHookPlan) error {
	if _, err := fmt.Fprintf(stdout, "projmux agent integrate antigravity (dry-run)\nhooks: %s\n", plan.path); err != nil {
		return err
	}
	if plan.executable != "" {
		if _, err := fmt.Fprintf(stdout, "source: %s\nevents: %s\n", plan.executable, strings.Join(antigravityManagedEvents, ", ")); err != nil {
			return err
		}
	}
	if plan.conflict != "" {
		_, err := fmt.Fprintf(stdout, "%s\n%s\n", plan.action, plan.conflict)
		return err
	}
	if strings.Contains(plan.current, antigravityLegacyIngestPath) {
		if _, err := fmt.Fprintf(stdout, "migration: %s -> %s\n", strings.TrimSpace(antigravityLegacyIngestPath), strings.TrimSpace(antigravityCanonicalIngestPath)); err != nil {
			return err
		}
	}
	if !plan.changed {
		_, err := fmt.Fprintln(stdout, plan.action)
		return err
	}
	entry, err := managedEntryFromDocument(plan.next)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s\nmanaged entry %q:\n%s\n", plan.action, antigravityManagedHookName, entry)
	return err
}

func managedEntryFromDocument(content string) (string, error) {
	return objectEntryFromDocument(content, antigravityManagedHookName)
}

func objectEntryFromDocument(content, key string) (string, error) {
	members, _, err := scanJSONObject(content)
	if err != nil {
		return "", err
	}
	for _, member := range members {
		if member.key == key {
			return content[member.valueFrom:member.valueTo], nil
		}
	}
	return "(removed)", nil
}

func scanJSONObject(content string) ([]jsonObjectMember, int, error) {
	if strings.TrimSpace(content) == "" {
		return nil, 0, errors.New("file is empty; expected a JSON object")
	}
	if !json.Valid([]byte(content)) {
		return nil, 0, errors.New("malformed JSON")
	}
	i := skipJSONWhitespace(content, 0)
	if i >= len(content) || content[i] != '{' {
		return nil, 0, errors.New("top-level value must be a JSON object")
	}
	i++
	members := []jsonObjectMember{}
	seen := map[string]bool{}
	for {
		i = skipJSONWhitespace(content, i)
		if i >= len(content) {
			return nil, 0, errors.New("unterminated JSON object")
		}
		if content[i] == '}' {
			return members, i, nil
		}
		memberFrom := i
		keyTo, err := scanJSONString(content, i)
		if err != nil {
			return nil, 0, err
		}
		var key string
		if err := json.Unmarshal([]byte(content[i:keyTo]), &key); err != nil {
			return nil, 0, fmt.Errorf("decode object key: %w", err)
		}
		if seen[key] {
			return nil, 0, fmt.Errorf("duplicate top-level key %q", key)
		}
		seen[key] = true
		i = skipJSONWhitespace(content, keyTo)
		if i >= len(content) || content[i] != ':' {
			return nil, 0, fmt.Errorf("object key %q is missing ':'", key)
		}
		i = skipJSONWhitespace(content, i+1)
		valueFrom := i
		valueTo, err := scanJSONValue(content, i)
		if err != nil {
			return nil, 0, fmt.Errorf("object key %q: %w", key, err)
		}
		members = append(members, jsonObjectMember{key: key, memberFrom: memberFrom, valueFrom: valueFrom, valueTo: valueTo})
		i = skipJSONWhitespace(content, valueTo)
		if i >= len(content) {
			return nil, 0, errors.New("unterminated JSON object")
		}
		switch content[i] {
		case ',':
			i++
		case '}':
			return members, i, nil
		default:
			return nil, 0, fmt.Errorf("unexpected character %q after object value", content[i])
		}
	}
}

func scanJSONString(content string, from int) (int, error) {
	if from >= len(content) || content[from] != '"' {
		return 0, errors.New("object key must be a JSON string")
	}
	escaped := false
	for i := from + 1; i < len(content); i++ {
		if escaped {
			escaped = false
			continue
		}
		switch content[i] {
		case '\\':
			escaped = true
		case '"':
			return i + 1, nil
		}
	}
	return 0, errors.New("unterminated JSON string")
}

func scanJSONValue(content string, from int) (int, error) {
	if from >= len(content) {
		return 0, errors.New("missing JSON value")
	}
	if content[from] == '"' {
		return scanJSONString(content, from)
	}
	if content[from] != '{' && content[from] != '[' {
		i := from
		for i < len(content) && content[i] != ',' && content[i] != '}' && content[i] != ']' && !isJSONWhitespace(content[i]) {
			i++
		}
		return i, nil
	}
	stack := []byte{content[from]}
	inString := false
	escaped := false
	for i := from + 1; i < len(content); i++ {
		ch := content[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}
		if ch == '"' {
			inString = true
			continue
		}
		switch ch {
		case '{', '[':
			stack = append(stack, ch)
		case '}', ']':
			open := stack[len(stack)-1]
			if (open == '{' && ch != '}') || (open == '[' && ch != ']') {
				return 0, errors.New("mismatched JSON container")
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return i + 1, nil
			}
		}
	}
	return 0, errors.New("unterminated JSON value")
}

func appendJSONObjectMember(content string, members []jsonObjectMember, closeAt int, key, value string) string {
	if len(members) == 0 {
		return "{\n  " + string(mustJSONMarshal(key)) + ": " + indentJSONValue(value, "  ") + "\n}\n"
	}
	last := members[len(members)-1]
	prefix := content[:last.valueTo]
	suffix := content[last.valueTo:]
	indent := jsonMemberIndent(content, members[0].memberFrom)
	separator := ","
	if strings.Contains(content[:closeAt], "\n") {
		separator += "\n" + indent
	}
	return prefix + separator + string(mustJSONMarshal(key)) + ": " + indentJSONValue(value, indent) + suffix
}

func removeJSONObjectMember(content string, members []jsonObjectMember, index int) string {
	member := members[index]
	if len(members) == 1 {
		return content[:member.memberFrom] + content[member.valueTo:]
	}
	if index < len(members)-1 {
		return content[:member.memberFrom] + content[members[index+1].memberFrom:]
	}
	previous := members[index-1]
	return content[:previous.valueTo] + content[member.valueTo:]
}

func jsonMemberIndent(content string, memberFrom int) string {
	lineFrom := strings.LastIndex(content[:memberFrom], "\n") + 1
	return content[lineFrom:memberFrom]
}

func indentJSONValue(value, indent string) string {
	return strings.ReplaceAll(value, "\n", "\n"+indent)
}

func mustJSONMarshal(value string) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func skipJSONWhitespace(content string, from int) int {
	for from < len(content) && isJSONWhitespace(content[from]) {
		from++
	}
	return from
}

func isJSONWhitespace(ch byte) bool {
	switch ch {
	case ' ', '\t', '\r', '\n':
		return true
	default:
		return false
	}
}
