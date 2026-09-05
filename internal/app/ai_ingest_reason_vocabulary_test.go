package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The reason column of ai-ingest.log is a closed vocabulary, and this is the
// gate that keeps it one.
//
// Every other surface in this diagnosis reads that column. A bounded token
// there is what lets a reader count, compare and classify; a subprocess message
// there is a sentence nobody can aggregate, and it carries whatever the
// subprocess felt like printing -- a socket path, a home directory, an exit
// status. This track's change boundary excludes exactly that.
//
// The static half matters more than the dynamic half. Injecting a failure and
// asserting the log stays clean only covers the paths a fixture actually
// drives; the sites that write a raw error are many, and hitting one of them
// leaves the rest open behind a green run. Reading the writes themselves
// covers all of them at once.

// aiIngestReasonForbiddenText are shapes that must never reach the column.
//
// They are the spellings observed in the field rather than a general secret
// scan: an absolute path, the transport label's own rendering, and the
// subprocess phrases that appear when a raw error is passed through.
var aiIngestReasonForbiddenText = []string{
	"/tmp/", "/home/", "-S/", "-L/",
	"exit status", "no server running", "error connecting to",
}

// aiIngestReasonRawValue reports whether one expression assigned to the reason
// column carries an unbounded value.
//
// Two shapes are refused. A call to Error() hands the column whatever an error
// chose to say, and a concatenation builds a sentence rather than naming a
// token. Everything else -- an identifier, a constant, a conversion of a
// vocabulary type -- is left alone: this gate cannot prove an identifier holds
// a bounded token, and pretending otherwise would make it noise.
func aiIngestReasonRawValue(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.CallExpr:
		if selector, ok := value.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Error" {
			return "err.Error()"
		}
		if selector, ok := value.Fun.(*ast.SelectorExpr); ok {
			if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "fmt" {
				return "fmt." + selector.Sel.Name
			}
		}
		return ""
	case *ast.BinaryExpr:
		if value.Op != token.ADD {
			return ""
		}
		if raw := aiIngestReasonRawValue(value.X); raw != "" {
			return raw + " (concatenated)"
		}
		if raw := aiIngestReasonRawValue(value.Y); raw != "" {
			return raw + " (concatenated)"
		}
		return ""
	case *ast.BasicLit:
		if value.Kind != token.STRING {
			return ""
		}
		text, err := strconv.Unquote(value.Value)
		if err != nil {
			return ""
		}
		for _, forbidden := range aiIngestReasonForbiddenText {
			if strings.Contains(text, forbidden) {
				return "literal containing " + strconv.Quote(forbidden)
			}
		}
		return ""
	default:
		return ""
	}
}

// aiIngestReasonWrites finds every reason value one file writes into the log.
func aiIngestReasonWrites(fileSet *token.FileSet, file *ast.File) []reasonWrite {
	var found []reasonWrite
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		name, ok := literal.Type.(*ast.Ident)
		if !ok || name.Name != "aiIngestLogEntry" {
			return true
		}
		for _, element := range literal.Elts {
			field, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := field.Key.(*ast.Ident)
			if !ok || key.Name != "Reason" {
				continue
			}
			if raw := aiIngestReasonRawValue(field.Value); raw != "" {
				found = append(found, reasonWrite{Position: fileSet.Position(field.Pos()), Shape: raw})
			}
		}
		return true
	})
	return found
}

type reasonWrite struct {
	Position token.Position
	Shape    string
}

// reasonSink is a function that builds a log entry whose reason comes from one
// of its own parameters.
//
// A sink is a second doorway into the column, and it is invisible to a scan
// that only reads entry literals: the literal inside the sink names an
// identifier, which is bounded as far as that file can tell, while every caller
// hands it whatever it likes. This is not hypothetical. One provider's path
// routes six raw errors through a sink today, and reading only the literals
// counted that provider as one violation instead of seven.
type reasonSink struct {
	Name  string
	Param int
}

// aiIngestReasonSinks finds the functions that pass a parameter into the reason
// column.
//
// One level of indirection is followed, not an arbitrary chain. A sink calling
// another sink would escape this, and closing that needs the value's type
// rather than its syntax -- which is the other half of this guarantee and
// belongs with whoever owns the vocabulary. What this closes is the doorway
// that is standing open.
func aiIngestReasonSinks(file *ast.File) []reasonSink {
	var sinks []reasonSink
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Type.Params == nil {
			continue
		}
		params := map[string]int{}
		index := 0
		for _, field := range function.Type.Params.List {
			for _, name := range field.Names {
				params[name.Name] = index
				index++
			}
		}
		ast.Inspect(function, func(node ast.Node) bool {
			literal, ok := node.(*ast.CompositeLit)
			if !ok {
				return true
			}
			name, ok := literal.Type.(*ast.Ident)
			if !ok || name.Name != "aiIngestLogEntry" {
				return true
			}
			for _, element := range literal.Elts {
				field, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := field.Key.(*ast.Ident)
				if !ok || key.Name != "Reason" {
					continue
				}
				value, ok := field.Value.(*ast.Ident)
				if !ok {
					continue
				}
				if position, bound := params[value.Name]; bound {
					sinks = append(sinks, reasonSink{Name: function.Name.Name, Param: position})
				}
			}
			return true
		})
	}
	return sinks
}

// aiIngestReasonSinkCalls classifies the argument each call hands to a sink.
func aiIngestReasonSinkCalls(fileSet *token.FileSet, file *ast.File, sinks map[string]int) []reasonWrite {
	var found []reasonWrite
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		var name string
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			name = fun.Name
		case *ast.SelectorExpr:
			name = fun.Sel.Name
		default:
			return true
		}
		param, known := sinks[name]
		if !known || param >= len(call.Args) {
			return true
		}
		if raw := aiIngestReasonRawValue(call.Args[param]); raw != "" {
			found = append(found, reasonWrite{
				Position: fileSet.Position(call.Args[param].Pos()),
				Shape:    raw + " via " + name + "()",
			})
		}
		return true
	})
	return found
}

// TestIngestReasonColumnCarriesOnlyBoundedValues is the static half.
func TestIngestReasonColumnCarriesOnlyBoundedValues(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	type parsedFile struct {
		name    string
		fileSet *token.FileSet
		file    *ast.File
	}
	var sources []parsedFile
	sinks := map[string]int{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		payload, readErr := os.ReadFile(path) // #nosec G304 -- package source under test.
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		fileSet := token.NewFileSet()
		parsed, parseErr := parser.ParseFile(fileSet, path, payload, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		sources = append(sources, parsedFile{name: name, fileSet: fileSet, file: parsed})
		for _, sink := range aiIngestReasonSinks(parsed) {
			sinks[sink.Name] = sink.Param
		}
	}
	var raw []string
	for _, source := range sources {
		writes := aiIngestReasonWrites(source.fileSet, source.file)
		writes = append(writes, aiIngestReasonSinkCalls(source.fileSet, source.file, sinks)...)
		for _, write := range writes {
			raw = append(raw, source.name+":"+strconv.Itoa(write.Position.Line)+": "+write.Shape)
		}
	}
	sort.Strings(raw)
	if len(raw) > 0 {
		t.Fatalf("%d reason write(s) carry an unbounded value:\n  %s\n\n"+
			"The reason column is a closed vocabulary every diagnosis surface reads. A subprocess message "+
			"there cannot be counted or compared, and it carries whatever that subprocess printed -- a socket "+
			"path, a home directory, an exit status. Name a bounded token instead.",
			len(raw), strings.Join(raw, "\n  "))
	}
}

// TestIngestReasonAuditRejectsARawValueItIsShown is the gate checking itself.
//
// A rule written down is not a rule that holds. This drives the classifier over
// each shape it is meant to refuse and each it is meant to leave alone, so the
// audit cannot quietly stop recognising the thing it exists to find.
func TestIngestReasonAuditRejectsARawValueItIsShown(t *testing.T) {
	const source = `package app

func sample(c *aiCommand, err error, reason string) {
	c.appendAIIngestLog(aiIngestLogEntry{Reason: err.Error()})
	c.appendAIIngestLog(aiIngestLogEntry{Reason: "route: " + err.Error()})
	c.appendAIIngestLog(aiIngestLogEntry{Reason: fmt.Sprintf("failed on %s", path)})
	c.appendAIIngestLog(aiIngestLogEntry{Reason: "no server running on the socket"})
	c.appendAIIngestLog(aiIngestLogEntry{Reason: reason})
	c.appendAIIngestLog(aiIngestLogEntry{Reason: aiPaneMatchReasonNoMatch})
	c.appendAIIngestLog(aiIngestLogEntry{Reason: string(codexObserverReasonReady)})
	c.appendAIIngestLog(aiIngestLogEntry{Reason: "pane inventory unavailable"})
}
`
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "sample.go", source, 0)
	if err != nil {
		t.Fatalf("parse sample: %v", err)
	}
	found := aiIngestReasonWrites(fileSet, parsed)
	if len(found) != 4 {
		shapes := make([]string, 0, len(found))
		for _, write := range found {
			shapes = append(shapes, strconv.Itoa(write.Position.Line)+":"+write.Shape)
		}
		t.Fatalf("classified %d write(s) as raw, want the error, the concatenation, the Sprintf and the "+
			"subprocess literal — and none of the bounded four\n%v", len(found), shapes)
	}
	for _, write := range found {
		if write.Position.Line < 4 || write.Position.Line > 7 {
			t.Fatalf("line %d was classified raw; the bounded writes start at line 8", write.Position.Line)
		}
	}
}

// TestIngestReasonAuditFollowsAReasonThroughItsSink is the second half of the
// gate checking itself.
//
// A scan that reads only entry literals sees a sink's inner literal name an
// identifier and calls it bounded, while every caller hands that identifier
// whatever it likes. One provider routes six raw errors that way today, and
// reading only the literals counted that provider as one violation instead of
// seven -- so the gate under-reported the very number it was asked to be the
// judgment of record for.
func TestIngestReasonAuditFollowsAReasonThroughItsSink(t *testing.T) {
	const source = `package app

func hookLogEntry(paneID string, result, reason string) aiIngestLogEntry {
	return aiIngestLogEntry{Pane: paneID, Result: result, Reason: reason}
}

func sample(c *aiCommand, err error) {
	c.appendAIIngestLog(hookLogEntry("%1", "error", err.Error()))
	c.appendAIIngestLog(hookLogEntry("%1", "ignored", aiPaneMatchReasonNoMatch))
}
`
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "sample.go", source, 0)
	if err != nil {
		t.Fatalf("parse sample: %v", err)
	}
	sinks := aiIngestReasonSinks(parsed)
	if len(sinks) != 1 || sinks[0].Name != "hookLogEntry" || sinks[0].Param != 2 {
		t.Fatalf("sinks = %+v, want the reason parameter of hookLogEntry at index 2", sinks)
	}
	// The literal inside the sink names a parameter, so the literal scan alone
	// must find nothing. That is exactly why the sink scan has to exist.
	if direct := aiIngestReasonWrites(fileSet, parsed); len(direct) != 0 {
		t.Fatalf("literal scan found %+v, want the sink to be invisible to it", direct)
	}
	found := aiIngestReasonSinkCalls(fileSet, parsed, map[string]int{"hookLogEntry": 2})
	if len(found) != 1 {
		t.Fatalf("sink scan found %d call(s), want only the one handing over a raw error", len(found))
	}
	if !strings.Contains(found[0].Shape, "via hookLogEntry()") {
		t.Fatalf("shape = %q, want the sink named so the caller can be found", found[0].Shape)
	}
}
