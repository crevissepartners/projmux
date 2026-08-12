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

	antigravitySettingsRelativePath    = ".gemini/antigravity-cli/settings.json"
	antigravityManagedStatusLineKey    = "statusLine"
	antigravityManagedStatusLineMarker = "projmux-managed:antigravity-statusline:v1"
)

var antigravityManagedEvents = []string{
	"PreInvocation",
	"PostInvocation",
	"PostToolUse",
	"Stop",
}

type antigravityHookPlan struct {
	path       string
	current    string
	next       string
	action     string
	executable string
	changed    bool
	conflict   string
}

type antigravityStatusLinePlan struct {
	path       string
	current    string
	next       string
	action     string
	executable string
	changed    bool
	managed    bool
	conflict   string
}

// antigravityManagedStatusLine is the official Antigravity CLI v1.1.12
// settings.json shape documented at https://antigravity.google/docs/cli/statusline.
// The installed v1.1.12 binary also carries the Go JSON tag
// `StackWithDefault json:"stack_with_default,omitempty"`. Stacking keeps the
// built-in line visible; the managed command deliberately writes no stdout.
type antigravityManagedStatusLine struct {
	Type             string `json:"type"`
	Command          string `json:"command"`
	Enabled          bool   `json:"enabled"`
	StackWithDefault bool   `json:"stack_with_default"`
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
	dryRun := fs.Bool("dry-run", false, "print planned Antigravity hook and statusline changes without writing")
	remove := fs.Bool("remove", false, "remove the projmux-managed Antigravity hook and statusline entries")
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
	statusLinePlan, err := c.planAntigravityStatusLineIntegration(*remove)
	if err != nil {
		return err
	}
	if *dryRun {
		return printAntigravityIntegrationDryRun(stdout, hookPlan, statusLinePlan)
	}
	if hookPlan.conflict != "" {
		return errors.New(hookPlan.conflict + "; run `projmux ai integrate antigravity --dry-run` to inspect without writing")
	}
	if statusLinePlan.conflict != "" {
		return errors.New(statusLinePlan.conflict + "; run `projmux ai integrate antigravity --dry-run` to inspect without writing")
	}
	// Preflight both separately owned files before either write so a known
	// permission failure cannot leave only half of the combined integration.
	for _, target := range []struct {
		path    string
		changed bool
		label   string
	}{
		{hookPlan.path, hookPlan.changed, "hooks"},
		{statusLinePlan.path, statusLinePlan.changed, "settings"},
	} {
		if target.changed {
			if err := preflightAntigravityWrite(target.path); err != nil {
				return fmt.Errorf("write Antigravity %s %s: %w", target.label, target.path, err)
			}
		}
	}
	if hookPlan.changed {
		if err := c.writeAntigravityJSON(hookPlan.path, []byte(hookPlan.next), 0o644, "hooks"); err != nil {
			return err
		}
	}
	if statusLinePlan.changed {
		if err := c.writeAntigravityJSON(statusLinePlan.path, []byte(statusLinePlan.next), 0o600, "settings"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(stdout, hookPlan.action); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, statusLinePlan.action)
	return err
}

func preflightAntigravityWrite(path string) error {
	current := path
	for {
		info, err := os.Stat(current)
		if err == nil {
			if info.Mode().Perm()&0o222 == 0 {
				return os.ErrPermission
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return os.ErrPermission
		}
		current = parent
	}
}

func (c *aiCommand) planAntigravityStatusLineIntegration(remove bool) (antigravityStatusLinePlan, error) {
	home, err := c.homeDir()
	if err != nil {
		return antigravityStatusLinePlan{}, fmt.Errorf("resolve home directory: %w", err)
	}
	path := filepath.Join(home, antigravitySettingsRelativePath)
	current, exists, err := c.readAntigravityJSON(home, path, "settings")
	if err != nil {
		return antigravityStatusLinePlan{}, err
	}
	plan := antigravityStatusLinePlan{path: path, current: current, next: current}
	if !exists {
		if remove {
			plan.action = "no changes: projmux-managed Antigravity statusline is not present in " + path
			return plan, nil
		}
		current = "{}\n"
	}
	members, closeAt, err := scanJSONObject(current)
	if err != nil {
		return antigravityStatusLinePlan{}, fmt.Errorf("parse Antigravity settings %s: %w", path, err)
	}
	statusIndex := -1
	for i, member := range members {
		if member.key != antigravityManagedStatusLineKey {
			continue
		}
		statusIndex = i
		value := current[member.valueFrom:member.valueTo]
		if isEmptyAntigravityStatusLine(value) {
			break
		}
		if !isManagedAntigravityStatusLine(value) {
			if remove {
				plan.action = "preserved unmanaged Antigravity statusline in " + path
				return plan, nil
			}
			plan.conflict = fmt.Sprintf("Antigravity settings %s already contains an unmanaged %q command; remove it with `/statusline delete` or choose which integration should own it", path, antigravityManagedStatusLineKey)
			plan.action = "would refuse to modify unmanaged Antigravity statusline"
			return plan, nil
		}
		plan.managed = true
		break
	}

	if remove {
		if !plan.managed {
			plan.action = "no changes: projmux-managed Antigravity statusline is not present in " + path
			return plan, nil
		}
		plan.next = removeJSONObjectMember(current, members, statusIndex)
		plan.changed = plan.next != plan.current
		plan.action = "removed projmux-managed Antigravity statusline from " + path
		return plan, nil
	}

	executable, err := c.persistentExecutablePath()
	if err != nil {
		return antigravityStatusLinePlan{}, fmt.Errorf("resolve stable absolute projmux executable for Antigravity statusline: %w", err)
	}
	plan.executable = executable
	entry, err := encodeAntigravityManagedStatusLine(executable)
	if err != nil {
		return antigravityStatusLinePlan{}, err
	}
	if statusIndex >= 0 {
		member := members[statusIndex]
		entry = indentJSONValue(entry, jsonMemberIndent(current, member.memberFrom))
		plan.next = current[:member.valueFrom] + entry + current[member.valueTo:]
	} else {
		plan.next = appendJSONObjectMember(current, members, closeAt, antigravityManagedStatusLineKey, entry)
	}
	plan.changed = plan.next != plan.current
	if plan.changed {
		plan.action = "configured Antigravity statusline in " + path
	} else {
		plan.action = "no changes: Antigravity statusline is already configured in " + path
	}
	return plan, nil
}

func isEmptyAntigravityStatusLine(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed == "null" || trimmed == "{}"
}

func isManagedAntigravityStatusLine(value string) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &raw); err != nil || len(raw) != 4 {
		return false
	}
	for _, key := range []string{"type", "command", "enabled", "stack_with_default"} {
		if _, ok := raw[key]; !ok {
			return false
		}
	}
	var decoded antigravityManagedStatusLine
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return false
	}
	return decoded.Type == "command" && decoded.Enabled && decoded.StackWithDefault &&
		isManagedAntigravityStatusLineCommand(decoded.Command)
}

func isManagedAntigravityStatusLineCommand(command string) bool {
	const suffix = " ai ingest antigravity-hook --event Statusline # " + antigravityManagedStatusLineMarker
	prefix := strings.TrimSuffix(strings.TrimSpace(command), suffix)
	if prefix == command || len(prefix) < 3 || prefix[0] != '\'' || prefix[len(prefix)-1] != '\'' {
		return false
	}
	executable := strings.ReplaceAll(prefix[1:len(prefix)-1], "'\\''", "'")
	return filepath.IsAbs(executable)
}

func encodeAntigravityManagedStatusLine(executable string) (string, error) {
	entry := antigravityManagedStatusLine{
		Type:             "command",
		Command:          shellQuote(executable) + " ai ingest antigravity-hook --event Statusline # " + antigravityManagedStatusLineMarker,
		Enabled:          true,
		StackWithDefault: true,
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode projmux-managed Antigravity statusline: %w", err)
	}
	return string(data), nil
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
	plan := antigravityHookPlan{path: path, current: current, next: current}
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
		if jsonHasCommandContaining(value, antigravityManagedMarker) || jsonHasCommandContaining(value, "projmux ai ingest antigravity-hook") {
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
	return shellQuote(executable) + " ai ingest antigravity-hook --event " + event +
		" || printf '%s\\n' '" + fallback + "' # " + antigravityManagedMarker
}

func printAntigravityHookDryRun(stdout io.Writer, plan antigravityHookPlan) error {
	if _, err := fmt.Fprintf(stdout, "projmux ai integrate antigravity (dry-run)\nhooks: %s\n", plan.path); err != nil {
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

func printAntigravityIntegrationDryRun(stdout io.Writer, hooks antigravityHookPlan, statusline antigravityStatusLinePlan) error {
	if err := printAntigravityHookDryRun(stdout, hooks); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "statusline settings: %s\n", statusline.path); err != nil {
		return err
	}
	if statusline.executable != "" {
		if _, err := fmt.Fprintf(stdout, "statusline source: %s\n", statusline.executable); err != nil {
			return err
		}
	}
	if statusline.conflict != "" {
		_, err := fmt.Fprintf(stdout, "%s\n%s\n", statusline.action, statusline.conflict)
		return err
	}
	if !statusline.changed {
		_, err := fmt.Fprintln(stdout, statusline.action)
		return err
	}
	entry, err := objectEntryFromDocument(statusline.next, antigravityManagedStatusLineKey)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s\nmanaged entry %q:\n%s\n", statusline.action, antigravityManagedStatusLineKey, entry)
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
