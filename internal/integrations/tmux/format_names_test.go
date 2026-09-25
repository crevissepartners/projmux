package tmux

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// This file guards against sending tmux a format variable that tmux does not
// define. tmux expands an unknown #{name} to an empty string without any
// error, so a typo such as #{client_active_pane} or #{mouse_window} fails
// silently at runtime. The guard parses every non-test Go file under
// internal/ and cmd/, assembles each string expression (resolving package
// constants), parses the tmux formats in it the way tmux 3.6 format.c does, and
// checks every name against the lists in testdata/: the format variables and
// option names extracted from the tmux man page, and the hand-maintained list
// of environment variable names projmux itself puts into the tmux environment
// (tmux formats may also name an environment variable).

// formatHole stands for a part of a string expression whose value is not
// statically known (a variable, a call, a field, ...).
const formatHole = '\x00'

const (
	formatVariablesFile = "testdata/tmux-format-variables.txt"
	formatOptionsFile   = "testdata/tmux-option-names.txt"
	formatEnvFile       = "testdata/tmux-environment-names.txt"
)

var formatPlainName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type formatFinding struct {
	Path string
	Line int
	Name string
}

func (f formatFinding) String() string {
	return fmt.Sprintf("%s:%d: unknown tmux format name %q", f.Path, f.Line, f.Name)
}

type formatUnresolved struct {
	Path   string
	Line   int
	Reason string
}

func (u formatUnresolved) String() string {
	return fmt.Sprintf("%s:%d: %s", u.Path, u.Line, u.Reason)
}

type formatScanReport struct {
	Files            int
	FilesWithFormats int
	NameOccurrences  int
	Names            map[string]int
	Unknown          []formatFinding
	Unresolved       []formatUnresolved
}

func (r *formatScanReport) summary() string {
	return fmt.Sprintf("tmux format guard: %d files scanned, %d files with formats, %d name occurrences, %d unique names, %d unresolved sites",
		r.Files, r.FilesWithFormats, r.NameOccurrences, len(r.Names), len(r.Unresolved))
}

// ---------------------------------------------------------------------------
// Known names

var (
	knownFormatNamesOnce sync.Once
	knownFormatNames     map[string]bool
	knownFormatNamesErr  error
)

func readFormatNameList(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var names []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		names = append(names, line)
	}
	return names, sc.Err()
}

func loadKnownFormatNames(t *testing.T) map[string]bool {
	t.Helper()
	knownFormatNamesOnce.Do(func() {
		known := map[string]bool{}
		for _, path := range []string{formatVariablesFile, formatOptionsFile, formatEnvFile} {
			names, err := readFormatNameList(path)
			if err != nil {
				knownFormatNamesErr = err
				return
			}
			for _, name := range names {
				known[name] = true
			}
		}
		knownFormatNames = known
	})
	if knownFormatNamesErr != nil {
		t.Fatalf("load tmux name lists: %v", knownFormatNamesErr)
	}
	return knownFormatNames
}

// ---------------------------------------------------------------------------
// Format parser (mirrors tmux 3.6 format.c)

// formatEvent is one thing the parser found at byte offset Off of the parsed
// string: a name candidate to check, or an unresolved site.
type formatEvent struct {
	Name       string
	Unresolved string
	Off        int
}

type formatParser struct {
	s      string
	events []formatEvent
}

// parseTmuxFormats returns every name candidate and unresolved site in s.
// A formatHole byte in s marks an unknown part of the string.
func parseTmuxFormats(s string) []formatEvent {
	p := &formatParser{s: s}
	p.expand(0, len(s))
	return p.events
}

func (p *formatParser) name(i, j int) {
	p.events = append(p.events, formatEvent{Name: p.s[i:j], Off: i})
}

func (p *formatParser) unresolved(off int, reason string) {
	p.events = append(p.events, formatEvent{Unresolved: reason, Off: off})
}

// skip mirrors format_skip: it returns the index of the first byte in
// s[i:j] that is in ends outside any nested #{...}, honoring the #, ## #{ #}
// and #: escapes, or -1.
func (p *formatParser) skip(i, j int, ends string) int {
	brackets := 0
	for k := i; k < j; k++ {
		c := p.s[k]
		if c == '#' && k+1 < j && p.s[k+1] == '{' {
			brackets++
		}
		if c == '#' && k+1 < j && strings.IndexByte(",#{}:", p.s[k+1]) >= 0 {
			k++
			continue
		}
		if c == '}' {
			brackets--
		}
		if strings.IndexByte(ends, c) >= 0 && brackets == 0 {
			return k
		}
	}
	return -1
}

// expand mirrors the top level of format_expand1 over s[i:j].
func (p *formatParser) expand(i, j int) {
	for k := i; k < j; {
		if p.s[k] != '#' || k+1 >= j {
			k++
			continue
		}
		if p.s[k+1] != '{' {
			// ##, #, and #} are literal characters, #[ starts a style
			// and #( a shell command whose contents are still scanned,
			// anything else is a single-character alias.
			k += 2
			continue
		}
		end := p.skip(k, j, "}")
		if end < 0 {
			p.unresolved(k, "#{ has no closing }")
			k += 2
			continue
		}
		p.format(k+2, end)
		k = end + 1
	}
}

func isCPunct(c byte) bool {
	return (c >= '!' && c <= '/') || (c >= ':' && c <= '@') || (c >= '[' && c <= '`') || (c >= '{' && c <= '~')
}

// modifiers mirrors format_build_modifiers over s[a:b]. It returns the
// modifier names, the argument ranges, the index where the body starts, and
// whether a modifier list terminated by ':' was found.
func (p *formatParser) modifiers(a, b int) (mods []string, args [][2]int, body int, ok bool) {
	s := p.s
	isEnd := func(k int) bool { return k < b && (s[k] == ';' || s[k] == ':') }
	cp := a
	for cp < b && s[cp] != ':' {
		if s[cp] == ';' {
			cp++
		}
		if cp >= b {
			break
		}
		c := s[cp]
		if strings.IndexByte("labcdnwETSWPLR!<>", c) >= 0 && isEnd(cp+1) {
			mods = append(mods, string(c))
			cp++
			continue
		}
		if cp+2 <= b {
			switch s[cp : cp+2] {
			case "||", "&&", "!!", "!=", "==", "<=", ">=":
				if isEnd(cp + 2) {
					mods = append(mods, s[cp:cp+2])
					cp += 2
					continue
				}
			}
		}
		if strings.IndexByte("mCNst=pReq", c) < 0 {
			break
		}
		if isEnd(cp + 1) {
			mods = append(mods, string(c))
			cp++
			continue
		}
		if cp+1 >= b {
			break
		}
		if !isCPunct(s[cp+1]) || s[cp+1] == '-' {
			end := p.skip(cp+1, b, ":;")
			if end < 0 {
				break
			}
			args = append(args, [2]int{cp + 1, end})
			mods = append(mods, string(c))
			cp = end
			continue
		}
		wrapper := s[cp+1]
		cp++
		for {
			if s[cp] == wrapper && isEnd(cp+1) {
				cp++
				break
			}
			end := p.skip(cp+1, b, string(wrapper)+";:")
			if end < 0 {
				break
			}
			args = append(args, [2]int{cp + 1, end})
			cp = end
			if isEnd(cp) {
				break
			}
		}
		mods = append(mods, string(c))
	}
	if cp >= b || s[cp] != ':' {
		return nil, nil, a, false
	}
	return mods, args, cp + 1, true
}

// format handles the content s[a:b] of one #{...}.
func (p *formatParser) format(a, b int) {
	mods, args, body, ok := p.modifiers(a, b)
	if ok {
		for _, arg := range args {
			p.expand(arg[0], arg[1])
		}
	}
	operands := false
	for _, m := range mods {
		switch m {
		case "l":
			// Literal: the body is not expanded.
			return
		case "==", "!=", "<", ">", "<=", ">=", "||", "&&", "!!", "!",
			"m", "e", "C", "N", "S", "W", "P", "L", "R", "a":
			// Operators, loops and searches: the body holds string
			// operands or sub-formats, not a variable name.
			operands = true
		}
	}
	if operands {
		p.expand(body, b)
		return
	}
	if body < b && p.s[body] == '?' {
		p.conditional(body+1, b)
		return
	}
	p.name(body, b)
}

// conditional handles #{?c1,a,c2,b,...,else} with s[a:b] after the '?'.
func (p *formatParser) conditional(a, b int) {
	var parts [][2]int
	for start := a; ; {
		end := p.skip(start, b, ",")
		if end < 0 {
			parts = append(parts, [2]int{start, b})
			break
		}
		parts = append(parts, [2]int{start, end})
		start = end + 1
	}
	n := len(parts)
	for idx, part := range parts {
		isCondition := idx%2 == 0 && idx < n-1
		if isCondition && !strings.Contains(p.s[part[0]:part[1]], "#{") {
			p.name(part[0], part[1])
			continue
		}
		p.expand(part[0], part[1])
	}
}

// classifyFormatName returns "" for an accepted name, "unknown" for a
// well-formed name tmux does not define, or an unresolved reason.
func classifyFormatName(name string, known map[string]bool) string {
	switch {
	case strings.HasPrefix(name, "@"):
		return ""
	case strings.ContainsRune(name, formatHole):
		return fmt.Sprintf("format name %q is built at runtime", strings.ReplaceAll(name, string(formatHole), "…"))
	case strings.Contains(name, "%"):
		return fmt.Sprintf("format name %q contains a fmt verb", name)
	case !formatPlainName.MatchString(name):
		return fmt.Sprintf("format name %q is not a plain name", name)
	case known[name]:
		return ""
	default:
		return "unknown"
	}
}

// ---------------------------------------------------------------------------
// Go source scanner

type formatGoFile struct {
	rel     string // slash path relative to the scan root
	dir     string // slash dir relative to the scan root
	file    *ast.File
	imports map[string]string // local name -> dir
}

type formatConst struct {
	expr ast.Expr
	file *formatGoFile
}

type formatScanner struct {
	root       string
	suffix     string // source file suffix: ".go", or ".go.txt" for fixtures
	modulePath string
	known      map[string]bool
	fset       *token.FileSet
	files      []*formatGoFile
	pkgNames   map[string]string                 // dir -> package name
	consts     map[string]map[string]formatConst // dir -> name -> decl
	constCache map[string]*string
	resolving  map[string]bool
	report     formatScanReport
	fileHits   map[string]bool
}

// scanTmuxFormats scans every non-test Go source file (name ending in suffix
// but not in "_test"+suffix) under root/<dir> for dirs.
func scanTmuxFormats(t *testing.T, root, modulePath, suffix string, dirs []string) *formatScanReport {
	t.Helper()
	s := &formatScanner{
		root:       root,
		suffix:     suffix,
		modulePath: modulePath,
		known:      loadKnownFormatNames(t),
		fset:       token.NewFileSet(),
		pkgNames:   map[string]string{},
		consts:     map[string]map[string]formatConst{},
		constCache: map[string]*string{},
		resolving:  map[string]bool{},
		fileHits:   map[string]bool{},
	}
	s.report.Names = map[string]int{}
	for _, dir := range dirs {
		if err := filepath.WalkDir(filepath.Join(root, dir), s.walk); err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	s.resolveImports()
	s.collectConsts()
	for _, f := range s.files {
		s.scanFile(f)
	}
	sort.SliceStable(s.report.Unknown, func(i, j int) bool {
		a, b := s.report.Unknown[i], s.report.Unknown[j]
		return a.Path < b.Path || (a.Path == b.Path && a.Line < b.Line)
	})
	sort.SliceStable(s.report.Unresolved, func(i, j int) bool {
		a, b := s.report.Unresolved[i], s.report.Unresolved[j]
		return a.Path < b.Path || (a.Path == b.Path && a.Line < b.Line)
	})
	s.report.Files = len(s.files)
	s.report.FilesWithFormats = len(s.fileHits)
	return &s.report
}

func (s *formatScanner) walk(path string, d os.DirEntry, err error) error {
	if err != nil {
		return err
	}
	name := d.Name()
	if d.IsDir() {
		if path != s.root && (name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
			return filepath.SkipDir
		}
		return nil
	}
	if !strings.HasSuffix(name, s.suffix) || strings.HasSuffix(name, "_test"+s.suffix) {
		return nil
	}
	file, err := parser.ParseFile(s.fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(s.root, path)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	dir := filepath.ToSlash(filepath.Dir(rel))
	s.pkgNames[dir] = file.Name.Name
	s.files = append(s.files, &formatGoFile{rel: rel, dir: dir, file: file})
	return nil
}

func (s *formatScanner) resolveImports() {
	for _, f := range s.files {
		f.imports = map[string]string{}
		for _, imp := range f.file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil || !strings.HasPrefix(path, s.modulePath+"/") {
				continue
			}
			dir := strings.TrimPrefix(path, s.modulePath+"/")
			local := s.pkgNames[dir]
			if imp.Name != nil {
				local = imp.Name.Name
			}
			if local != "" && local != "_" && local != "." {
				f.imports[local] = dir
			}
		}
	}
}

func (s *formatScanner) collectConsts() {
	for _, f := range s.files {
		for _, decl := range f.file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				if len(vs.Values) != len(vs.Names) {
					continue
				}
				for i, name := range vs.Names {
					if s.consts[f.dir] == nil {
						s.consts[f.dir] = map[string]formatConst{}
					}
					s.consts[f.dir][name.Name] = formatConst{expr: vs.Values[i], file: f}
				}
			}
		}
	}
}

// resolveConst returns the string value of the package-level constant name in
// dir, if it is statically known.
func (s *formatScanner) resolveConst(dir, name string) (string, bool) {
	key := dir + "\x00" + name
	if v, ok := s.constCache[key]; ok {
		if v == nil {
			return "", false
		}
		return *v, true
	}
	decl, ok := s.consts[dir][name]
	if !ok || s.resolving[key] {
		return "", false
	}
	s.resolving[key] = true
	v, ok := s.evalConst(decl.expr, decl.file)
	delete(s.resolving, key)
	if ok {
		s.constCache[key] = &v
	} else {
		s.constCache[key] = nil
	}
	return v, ok
}

func (s *formatScanner) evalConst(e ast.Expr, f *formatGoFile) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(x.Value)
		return v, err == nil
	case *ast.ParenExpr:
		return s.evalConst(x.X, f)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		l, ok := s.evalConst(x.X, f)
		if !ok {
			return "", false
		}
		r, ok := s.evalConst(x.Y, f)
		return l + r, ok
	case *ast.Ident:
		return s.resolveConst(f.dir, x.Name)
	case *ast.SelectorExpr:
		pkg, ok := x.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		dir, ok := f.imports[pkg.Name]
		if !ok {
			return "", false
		}
		return s.resolveConst(dir, x.Sel.Name)
	}
	return "", false
}

func flattenFormatAdd(e ast.Expr, out []ast.Expr) []ast.Expr {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return flattenFormatAdd(x.X, out)
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			return flattenFormatAdd(x.Y, flattenFormatAdd(x.X, out))
		}
	}
	return append(out, e)
}

func isFormatStringLit(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Kind == token.STRING
}

func (s *formatScanner) scanFile(f *formatGoFile) {
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.ImportSpec:
			return false
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				s.scanRoot(f, []ast.Expr{x}, visit)
			}
			return false
		case *ast.BinaryExpr:
			if x.Op != token.ADD {
				return true
			}
			operands := flattenFormatAdd(x, nil)
			if slices.ContainsFunc(operands, isFormatStringLit) {
				s.scanRoot(f, operands, visit)
				return false
			}
		}
		return true
	}
	ast.Inspect(f.file, visit)
}

// scanRoot assembles one root string expression and checks the formats in it.
func (s *formatScanner) scanRoot(f *formatGoFile, operands []ast.Expr, visit func(ast.Node) bool) {
	var text strings.Builder
	var lines []int
	add := func(v string, line int, raw bool) {
		for i := 0; i < len(v); i++ {
			text.WriteByte(v[i])
			lines = append(lines, line)
			if raw && v[i] == '\n' {
				line++
			}
		}
	}
	for _, op := range operands {
		line := s.fset.Position(op.Pos()).Line
		if lit, ok := op.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				add(string(formatHole), line, false)
				continue
			}
			add(v, line, strings.HasPrefix(lit.Value, "`"))
			continue
		}
		if v, ok := s.evalConst(op, f); ok {
			add(v, line, false)
			continue
		}
		add(string(formatHole), line, false)
		ast.Inspect(op, visit)
	}
	str := text.String()
	if !strings.Contains(str, "#{") {
		return
	}
	s.fileHits[f.rel] = true
	for _, ev := range parseTmuxFormats(str) {
		line := lines[ev.Off]
		if ev.Unresolved != "" {
			s.report.Unresolved = append(s.report.Unresolved, formatUnresolved{Path: f.rel, Line: line, Reason: ev.Unresolved})
			continue
		}
		switch reason := classifyFormatName(ev.Name, s.known); reason {
		case "":
			s.report.NameOccurrences++
			s.report.Names[ev.Name]++
		case "unknown":
			s.report.Unknown = append(s.report.Unknown, formatFinding{Path: f.rel, Line: line, Name: ev.Name})
		default:
			s.report.Unresolved = append(s.report.Unresolved, formatUnresolved{Path: f.rel, Line: line, Reason: reason})
		}
	}
}

// ---------------------------------------------------------------------------
// Tests

func formatRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root %s has no go.mod: %v", root, err)
	}
	return root
}

func formatModulePath(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("no module line in %s/go.mod", root)
	return ""
}

func TestTmuxFormatNamesInRepositoryAreKnown(t *testing.T) {
	root := formatRepoRoot(t)
	report := scanTmuxFormats(t, root, formatModulePath(t, root), ".go", []string{"internal", "cmd"})

	t.Log(report.summary())
	for _, u := range report.Unresolved {
		t.Logf("unresolved: %s", u)
	}

	if report.Files == 0 || report.FilesWithFormats == 0 || report.NameOccurrences == 0 {
		t.Fatalf("guard scanned nothing: %s", report.summary())
	}
	for _, name := range []string{"mouse_status_range", "client_tty", "pane_id"} {
		if report.Names[name] == 0 {
			t.Errorf("positive control: name %q was not collected from the repository", name)
		}
	}
	for _, f := range report.Unknown {
		t.Errorf("%s", f)
	}
}

// The fixtures under testdata/formatguard are Go source named *.go.txt:
// `gosec ./...` type-checks testdata Go files and the fixtures import a fake
// module, so a .go name would break the security scan.
func TestTmuxFormatGuardRejectsUnknownNames(t *testing.T) {
	root := filepath.Join("testdata", "formatguard")
	report := scanTmuxFormats(t, root, "example.com/formatguard", ".go.txt", []string{"internal", "cmd"})
	t.Log(report.summary())

	want := []formatFinding{
		{Path: "cmd/tool/main.go.txt", Line: 8, Name: "mouse_window"},
		{Path: "internal/app/app.go.txt", Line: 12, Name: "client_active_pane"},
		{Path: "internal/app/app.go.txt", Line: 18, Name: "no_such_var_xyz"},
	}
	if !reflect.DeepEqual(report.Unknown, want) {
		t.Errorf("findings = %v, want %v", report.Unknown, want)
	}

	wantUnresolved := []formatUnresolved{
		{Path: "internal/app/app.go.txt", Line: 29, Reason: `format name "…" is built at runtime`},
		{Path: "internal/app/app.go.txt", Line: 33, Reason: `format name "%s" contains a fmt verb`},
	}
	if !reflect.DeepEqual(report.Unresolved, wantUnresolved) {
		t.Errorf("unresolved = %v, want %v", report.Unresolved, wantUnresolved)
	}

	// Constants resolved across packages and import aliases.
	for _, name := range []string{"pane_id", "@projmux_fixture_x", "session_name", "mouse"} {
		if report.Names[name] == 0 {
			t.Errorf("name %q was not collected from the fixture; names = %v", name, report.Names)
		}
	}
	if report.Files != 4 {
		t.Errorf("files scanned = %d, want 4 (test files and testdata skipped)", report.Files)
	}
}

func TestTmuxFormatGuardAcceptsOptionNamesAndUserOptions(t *testing.T) {
	known := loadKnownFormatNames(t)
	for _, input := range []string{
		"#{mouse}",
		"#{@projmux_anything}",
		"#{E:status-left-style}",
		"#{T;=/#{status-left-length}:status-left}",
		"#{?mouse,on,off}",
		"#{@projmux_" + string(formatHole) + "}",
		"#{E:__projmux_create_operation}",
		"#{E:__projmux_finalize_operation}",
	} {
		for _, ev := range parseTmuxFormats(input) {
			if ev.Unresolved != "" {
				t.Errorf("%q: unexpected unresolved site: %s", input, ev.Unresolved)
				continue
			}
			if reason := classifyFormatName(ev.Name, known); reason != "" {
				t.Errorf("%q: name %q rejected: %s", input, ev.Name, reason)
			}
		}
	}
}

func TestTmuxFormatGuardClassifiesEnvironmentNames(t *testing.T) {
	known := loadKnownFormatNames(t)
	for _, tt := range []struct {
		in   string
		want string
	}{
		{in: "#{E:__projmux_create_operation}", want: ""},
		{in: "#{__projmux_finalize_operation}", want: ""},
		{in: "#{E:__projmux_create_operatio}", want: "unknown"},
	} {
		events := parseTmuxFormats(tt.in)
		if len(events) != 1 || events[0].Unresolved != "" {
			t.Fatalf("%q: events = %+v, want one name", tt.in, events)
		}
		if got := classifyFormatName(events[0].Name, known); got != tt.want {
			t.Errorf("%q: classify %q = %q, want %q", tt.in, events[0].Name, got, tt.want)
		}
	}
}

func TestTmuxFormatParserExtractsNames(t *testing.T) {
	const hole = string(formatHole)
	tests := []struct {
		in         string
		names      []string
		unresolved []string
	}{
		{in: "plain text", names: nil},
		{in: "#{pane_id}", names: []string{"pane_id"}},
		{in: "#{q:pane_title}", names: []string{"pane_title"}},
		{in: "#{=/9/...:session_name}", names: []string{"session_name"}},
		{in: "#{=10:window_name}", names: []string{"window_name"}},
		{in: "#{=-10:window_name}", names: []string{"window_name"}},
		{in: "#{p10:pane_title}", names: []string{"pane_title"}},
		{in: "#{t/f/%%H#:%%M:window_activity}", names: []string{"window_activity"}},
		{in: "#{T;=/#{status-left-length}:status-left}", names: []string{"status-left-length", "status-left"}},
		{in: "#{s|^[^-]*-||:session_name}", names: []string{"session_name"}},
		{in: "#{s/a/b/:pane_title}", names: []string{"pane_title"}},
		{in: "#{m/r:^%[0-9]+$,#{pane_id}}", names: []string{"pane_id"}},
		{in: "#{m:*foo*,#{host}}", names: []string{"host"}},
		{in: "#{==:#{session_name},main}", names: []string{"session_name"}},
		{in: "#{!=:#{pane_id},}", names: []string{"pane_id"}},
		{in: "#{||:#{pane_in_mode},#{alternate_on}}", names: []string{"pane_in_mode", "alternate_on"}},
		{in: "#{&&:#{pane_active},#{window_active}}", names: []string{"pane_active", "window_active"}},
		{in: "#{e|<:#{window_index},10}", names: []string{"window_index"}},
		{in: "#{e|*|f|4:5.5,3}", names: nil},
		{in: "#{!:#{pane_in_mode}}", names: []string{"pane_in_mode"}},
		{in: "#{!!:non-empty string}", names: nil},
		{in: "#{R:a,3}", names: nil},
		{in: "#{?pane_active,yes,no}", names: []string{"pane_active"}},
		{in: "#{?pane_active,#{pane_id},no}", names: []string{"pane_active", "pane_id"}},
		{in: "#{?session_format,format1,window_format,format2,format3}", names: []string{"session_format", "window_format"}},
		{in: "#{?pane_active,yes,window_active,maybe}", names: []string{"pane_active", "window_active"}},
		{in: "#{?#{==:#{pane_id},%1},a,b}", names: []string{"pane_id"}},
		{in: "#{?pane_in_mode,#[fg=white#,bg=red],#[fg=red#,bg=white]}#W", names: []string{"pane_in_mode"}},
		{in: "#{W:#{E:window-status-format} ,#{E:window-status-current-format} }", names: []string{"window-status-format", "window-status-current-format"}},
		{in: "#{S:#{session_name} }", names: []string{"session_name"}},
		{in: "#{l:#{not_expanded}}", names: nil},
		{in: "##{not_a_format}", names: nil},
		{in: "#[fg=#{pane_fg}]text#[default]", names: []string{"pane_fg"}},
		{in: "#(echo #{pane_pid})", names: []string{"pane_pid"}},
		{in: "#{client_tty}#{mouse_status_range}", names: []string{"client_tty", "mouse_status_range"}},
		{in: "#{" + hole + "}", names: []string{hole}},
		{in: "#{@opt_" + hole + "}", names: []string{"@opt_" + hole}},
		{in: "#{%s}", names: []string{"%s"}},
		{in: "#{pane_id", unresolved: []string{"#{ has no closing }"}},
		{in: "#{?#{pane_id", unresolved: []string{"#{ has no closing }", "#{ has no closing }"}},
	}
	for _, tt := range tests {
		var names, unresolved []string
		for _, ev := range parseTmuxFormats(tt.in) {
			if ev.Unresolved != "" {
				unresolved = append(unresolved, ev.Unresolved)
			} else {
				names = append(names, ev.Name)
			}
		}
		if !reflect.DeepEqual(names, tt.names) || !reflect.DeepEqual(unresolved, tt.unresolved) {
			t.Errorf("parse %q: names %q unresolved %q, want names %q unresolved %q", tt.in, names, unresolved, tt.names, tt.unresolved)
		}
	}
}

func TestTmuxFormatNameListsAreSortedAndKnown(t *testing.T) {
	for _, tt := range []struct {
		path      string
		positives []string
	}{
		{path: formatVariablesFile, positives: []string{"pane_id", "client_tty", "mouse_status_range", "session_name"}},
		{path: formatEnvFile, positives: []string{"__projmux_create_operation", "__projmux_finalize_operation"}},
		{path: formatOptionsFile, positives: []string{"mouse", "status-left", "status-left-length", "status-right-length", "status-justify", "window-status-style", "window-status-separator", "window-status-current-format"}},
	} {
		names, err := readFormatNameList(tt.path)
		if err != nil {
			t.Fatal(err)
		}
		if len(names) == 0 {
			t.Errorf("%s is empty", tt.path)
		}
		set := map[string]bool{}
		for i, name := range names {
			if !formatPlainName.MatchString(name) {
				t.Errorf("%s: malformed name %q", tt.path, name)
			}
			if i > 0 && names[i-1] >= name {
				t.Errorf("%s: %q is not sorted after %q or is a duplicate", tt.path, name, names[i-1])
			}
			set[name] = true
		}
		for _, name := range tt.positives {
			if !set[name] {
				t.Errorf("%s is missing %q", tt.path, name)
			}
		}
		for _, name := range []string{"client_active_pane", "mouse_window"} {
			if set[name] {
				t.Errorf("%s lists %q, which tmux does not define", tt.path, name)
			}
		}
	}
}
