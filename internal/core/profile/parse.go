package profile

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// A profile file is a narrow, strict subset of TOML. go.mod carries no TOML
// library, and the vocabulary is small enough that a parser which accepts
// exactly what it understands is simpler than a general one that has to be
// narrowed afterwards. Accepted:
//
//   - blank lines, and `#` comments on their own line or after a value;
//   - top-level `key = value` lines with a bare key ([A-Za-z0-9_-]+);
//   - at most one `[permissions]` table header; every key after it belongs to
//     that table, as in TOML;
//   - values that are a basic double-quoted string, or an array of them. The
//     only escapes are \" and \\; a raw control character (tab included) or
//     newline inside a string is refused. An array may span lines, may hold
//     comments between elements, and may end with a trailing comma.
//
// Everything else is refused: quoted or dotted keys, other tables, a second
// [permissions], a key given twice, literal and multi-line strings, numbers,
// booleans, inline tables, nested arrays, and any key outside the vocabulary.

// Top-level keys.
const (
	keyInstructions = "instructions"
	keyModel        = "model"
	keyEffort       = "effort"
	keyRoles        = "roles"
)

// permissionsTable is the one table a profile may declare.
const permissionsTable = "permissions"

// [permissions] keys.
const (
	keySandbox  = "sandbox"
	keyApproval = "approval"
	keyAllow    = "allow"
	keyDeny     = "deny"
)

// EffortLevels is the accepted `effort` vocabulary. It is the set Claude's
// --effort takes; a test in internal/app holds it equal to the launch-option
// check there, which this package cannot import.
var EffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// ModelName is the accepted `model` shape: an alias such as "opus" or a full
// name such as "claude-opus-5" or "opus[1m]". A test in internal/app holds it
// equal to the launch-option check there.
var ModelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\[\]-]{0,63}$`)

// sandboxModes and approvalModes are the [permissions] vocabularies.
var (
	sandboxModes  = []string{"read-only", "workspace-write", "full-access"}
	approvalModes = []string{"never", "on-request", "untrusted"}
)

// permissionRule is the documented shape check for one Claude permission rule:
// a tool name, optionally followed by one non-empty parenthesized specifier
// that runs to the end of the rule, such as `Edit`, `Bash(git status *)`,
// `Read(./docs/**)`, `WebFetch(domain:example.com)`, or `mcp__srv__tool`. The
// parser has already refused control characters, so the specifier is any
// text. It is a shape check only: whether Claude knows the tool is not
// decided here.
var permissionRule = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*(\(.+\))?$`)

// Spec is one parsed, vocabulary-checked profile. Empty fields were absent.
type Spec struct {
	Instructions string
	Model        string
	Effort       string
	Roles        []string
	Permissions  Permissions
}

// Permissions is the [permissions] table.
type Permissions struct {
	Sandbox  string
	Approval string
	Allow    []string
	Deny     []string
}

// value is one parsed right-hand side: a string or an array of strings.
type value struct {
	str     string
	list    []string
	isArray bool
}

// Parse reads content as a profile file and checks every value against the
// vocabulary. It does not consult any other file: whether the named
// instructions exist and whether another profile already claims a role are
// the Store's checks.
func Parse(content []byte) (Spec, error) {
	if !utf8.Valid(content) {
		return Spec{}, &Error{Reason: ReasonSyntax, Detail: "is not valid UTF-8"}
	}
	p := &parser{src: string(content), line: 1}
	return p.parse()
}

type parser struct {
	src  string
	pos  int
	line int
}

func (p *parser) fail(reason, format string, args ...any) error {
	return &Error{Reason: reason, Detail: fmt.Sprintf("line %d: ", p.line) + fmt.Sprintf(format, args...)}
}

func (p *parser) eof() bool { return p.pos >= len(p.src) }

func (p *parser) peek() byte { return p.src[p.pos] }

func (p *parser) skipSpaces() {
	for !p.eof() && (p.peek() == ' ' || p.peek() == '\t') {
		p.pos++
	}
}

// newline consumes one "\n" or "\r\n" and reports whether it did.
func (p *parser) newline() bool {
	switch {
	case strings.HasPrefix(p.src[p.pos:], "\n"):
		p.pos++
	case strings.HasPrefix(p.src[p.pos:], "\r\n"):
		p.pos += 2
	default:
		return false
	}
	p.line++
	return true
}

// comment consumes a `#` comment up to, not including, the line end.
func (p *parser) comment() error {
	p.pos++
	for !p.eof() && p.peek() != '\n' && !strings.HasPrefix(p.src[p.pos:], "\r\n") {
		if isControl(p.peek()) && p.peek() != '\t' {
			return p.fail(ReasonSyntax, "control character in a comment")
		}
		p.pos++
	}
	return nil
}

// lineEnd accepts trailing spaces, an optional comment, then a line end or the
// end of the file.
func (p *parser) lineEnd() error {
	p.skipSpaces()
	if !p.eof() && p.peek() == '#' {
		if err := p.comment(); err != nil {
			return err
		}
	}
	if p.eof() || p.newline() {
		return nil
	}
	return p.fail(ReasonSyntax, "unexpected %q after the value", p.src[p.pos:p.pos+1])
}

func (p *parser) parse() (Spec, error) {
	var spec Spec
	table := ""
	seenTable := false
	seen := map[string]bool{}
	for {
		p.skipSpaces()
		if p.eof() {
			return spec, nil
		}
		switch c := p.peek(); {
		case p.newline():
		case c == '#':
			if err := p.comment(); err != nil {
				return Spec{}, err
			}
		case c == '[':
			name, err := p.tableHeader()
			if err != nil {
				return Spec{}, err
			}
			if name != permissionsTable {
				return Spec{}, p.fail(ReasonTableUnknown, "unknown table [%s]; the only table is [%s]", name, permissionsTable)
			}
			if seenTable {
				return Spec{}, p.fail(ReasonSyntax, "table [%s] is declared twice", name)
			}
			seenTable, table = true, name
			if err := p.lineEnd(); err != nil {
				return Spec{}, err
			}
		case isBareKey(c):
			key := p.bareKey()
			qualified := key
			if table != "" {
				qualified = table + "." + key
			}
			if !knownKey(table, key) {
				return Spec{}, p.fail(ReasonKeyUnknown, "unknown key %q", qualified)
			}
			if seen[qualified] {
				return Spec{}, p.fail(ReasonSyntax, "key %q is given twice", qualified)
			}
			seen[qualified] = true
			p.skipSpaces()
			if p.eof() || p.peek() != '=' {
				return Spec{}, p.fail(ReasonSyntax, "key %q must be followed by \"=\"", qualified)
			}
			p.pos++
			p.skipSpaces()
			val, err := p.value()
			if err != nil {
				return Spec{}, err
			}
			if err := p.assign(&spec, table, key, val); err != nil {
				return Spec{}, err
			}
			if err := p.lineEnd(); err != nil {
				return Spec{}, err
			}
		default:
			return Spec{}, p.fail(ReasonSyntax, "unexpected %q; expected a key, a table header, or a comment", p.src[p.pos:p.pos+1])
		}
	}
}

// tableHeader parses `[ name ]` with a bare name.
func (p *parser) tableHeader() (string, error) {
	p.pos++
	if !p.eof() && p.peek() == '[' {
		return "", p.fail(ReasonSyntax, "arrays of tables are not supported")
	}
	p.skipSpaces()
	if p.eof() || !isBareKey(p.peek()) {
		return "", p.fail(ReasonSyntax, "a table header must be [name] with a bare name")
	}
	name := p.bareKey()
	p.skipSpaces()
	if p.eof() || p.peek() != ']' {
		return "", p.fail(ReasonSyntax, "a table header must be [name] with a bare name")
	}
	p.pos++
	return name, nil
}

func (p *parser) bareKey() string {
	start := p.pos
	for !p.eof() && isBareKey(p.peek()) {
		p.pos++
	}
	return p.src[start:p.pos]
}

func (p *parser) value() (value, error) {
	if p.eof() {
		return value{}, p.fail(ReasonSyntax, "missing value")
	}
	switch p.peek() {
	case '"':
		s, err := p.basicString()
		return value{str: s}, err
	case '[':
		list, err := p.array()
		return value{list: list, isArray: true}, err
	default:
		return value{}, p.fail(ReasonValueInvalid, "a value must be a double-quoted string or an array of them")
	}
}

func (p *parser) basicString() (string, error) {
	if strings.HasPrefix(p.src[p.pos:], `"""`) {
		return "", p.fail(ReasonSyntax, "multi-line strings are not supported")
	}
	p.pos++
	var b strings.Builder
	for {
		if p.eof() {
			return "", p.fail(ReasonSyntax, "unterminated string")
		}
		c := p.peek()
		switch {
		case c == '"':
			p.pos++
			return b.String(), nil
		case c == '\\':
			if p.pos+1 >= len(p.src) {
				return "", p.fail(ReasonSyntax, "unterminated string")
			}
			next := p.src[p.pos+1]
			if next != '"' && next != '\\' {
				return "", p.fail(ReasonSyntax, "unsupported escape \\%c; only \\\" and \\\\ are accepted", next)
			}
			b.WriteByte(next)
			p.pos += 2
		case isControl(c):
			return "", p.fail(ReasonSyntax, "control character or line break inside a string")
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
}

// array parses `[ "a", "b", ]`, which may span lines and hold comments.
func (p *parser) array() ([]string, error) {
	p.pos++
	list := []string{}
	for {
		if err := p.arraySpace(); err != nil {
			return nil, err
		}
		if p.eof() {
			return nil, p.fail(ReasonSyntax, "unterminated array")
		}
		if p.peek() == ']' {
			p.pos++
			return list, nil
		}
		if p.peek() != '"' {
			return nil, p.fail(ReasonValueInvalid, "an array element must be a double-quoted string")
		}
		s, err := p.basicString()
		if err != nil {
			return nil, err
		}
		list = append(list, s)
		if err := p.arraySpace(); err != nil {
			return nil, err
		}
		if p.eof() {
			return nil, p.fail(ReasonSyntax, "unterminated array")
		}
		switch p.peek() {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return list, nil
		default:
			return nil, p.fail(ReasonSyntax, "array elements must be separated by \",\"")
		}
	}
}

// arraySpace skips spaces, line ends, and comments between array elements.
func (p *parser) arraySpace() error {
	for {
		p.skipSpaces()
		if p.eof() {
			return nil
		}
		if p.peek() == '#' {
			if err := p.comment(); err != nil {
				return err
			}
			continue
		}
		if !p.newline() {
			return nil
		}
	}
}

func knownKey(table, key string) bool {
	switch table {
	case "":
		return slices.Contains([]string{keyInstructions, keyModel, keyEffort, keyRoles}, key)
	case permissionsTable:
		return slices.Contains([]string{keySandbox, keyApproval, keyAllow, keyDeny}, key)
	}
	return false
}

// assign checks one value's type and vocabulary and stores it in spec.
func (p *parser) assign(spec *Spec, table, key string, val value) error {
	qualified := key
	if table != "" {
		qualified = table + "." + key
	}
	wantArray := key == keyRoles || key == keyAllow || key == keyDeny
	if val.isArray != wantArray {
		kind := "a string"
		if wantArray {
			kind = "an array of strings"
		}
		return p.fail(ReasonValueInvalid, "%q must be %s", qualified, kind)
	}
	oneOf := func(set []string) error {
		if slices.Contains(set, val.str) {
			return nil
		}
		return p.fail(ReasonValueInvalid, "%q is %q; want one of %s", qualified, val.str, strings.Join(set, ", "))
	}
	switch qualified {
	case keyInstructions:
		if err := persona.ValidateName(val.str); err != nil {
			return p.fail(ReasonValueInvalid, "%q is not a valid instructions name: %v", qualified, err)
		}
		spec.Instructions = val.str
	case keyModel:
		if !ModelName.MatchString(val.str) {
			return p.fail(ReasonValueInvalid, "%q is %q; want a model alias or name matching %s", qualified, val.str, ModelName)
		}
		spec.Model = val.str
	case keyEffort:
		if err := oneOf(EffortLevels); err != nil {
			return err
		}
		spec.Effort = val.str
	case keyRoles:
		seen := map[string]bool{}
		for _, role := range val.list {
			if err := validateRole(role); err != nil {
				return p.fail(ReasonValueInvalid, "%q: %v", qualified, err)
			}
			if seen[role] {
				return p.fail(ReasonRoleDuplicate, "role %q is listed twice", role)
			}
			seen[role] = true
		}
		spec.Roles = val.list
	case permissionsTable + "." + keySandbox:
		if err := oneOf(sandboxModes); err != nil {
			return err
		}
		spec.Permissions.Sandbox = val.str
	case permissionsTable + "." + keyApproval:
		if err := oneOf(approvalModes); err != nil {
			return err
		}
		spec.Permissions.Approval = val.str
	case permissionsTable + "." + keyAllow, permissionsTable + "." + keyDeny:
		for _, rule := range val.list {
			if !permissionRule.MatchString(rule) {
				return p.fail(ReasonValueInvalid, "%q rule %q is not Tool or Tool(specifier)", qualified, rule)
			}
		}
		if key == keyAllow {
			spec.Permissions.Allow = val.list
		} else {
			spec.Permissions.Deny = val.list
		}
	}
	return nil
}

// validateRole applies the label-value rule a role must meet to be selectable
// later: it is non-empty and survives the `--selector role=<value>` grammar
// (selector.ParseLabel) unchanged, so it has no surrounding whitespace.
func validateRole(role string) error {
	if role == "" {
		return fmt.Errorf("a role must not be empty")
	}
	label, err := selector.ParseLabel("role=" + role)
	if err != nil {
		return err
	}
	if label.Value != role {
		return fmt.Errorf("role %q must not have surrounding whitespace", role)
	}
	return nil
}

func isBareKey(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

func isControl(c byte) bool { return c < 0x20 || c == 0x7f }
