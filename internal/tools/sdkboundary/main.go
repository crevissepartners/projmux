// sdkboundary checks implementation dependencies and the complete exported Go
// type graph. It uses compiler export data, including aliases and generics.
package main

import (
	"encoding/json"
	"fmt"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const module = "github.com/crevissepartners/projmux"

type packageData struct {
	ImportPath string
	Export     string
	Standard   bool
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: sdkboundary <package>")
		os.Exit(2)
	}
	if err := check(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func check(path string) error {
	return checkPackage(path, path == module+"/pkg/projmux/core")
}

func checkPackage(path string, pureCore bool) error {
	goBinary := os.Getenv("GO")
	if goBinary == "" {
		goBinary = "go"
	}
	command := exec.Command(goBinary, "list", "-deps", "-export", "-json", path) // #nosec G204 G702 -- build-time tool: operator selects GO and one package argument; no shell interpolation or product request input
	command.Stderr = os.Stderr
	output, err := command.Output()
	if err != nil {
		return err
	}
	data := map[string]packageData{}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	for {
		var p packageData
		err := decoder.Decode(&p)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		data[p.ImportPath] = p
	}
	// An exact allowlist freezes this slice's implementation dependencies.
	// New dependencies require explicit review, including through a service.
	names := make([]string, 0, len(data))
	for name := range data {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name != path && (pureCore && !data[name].Standard || !allowedImplementation(data[name])) {
			return fmt.Errorf("SDK implementation import is not allowed: %q", name)
		}
	}
	lookup := func(name string) (io.ReadCloser, error) {
		return os.Open(data[name].Export) // #nosec G304 -- compiler-generated export archive from go list, not a product input
	}
	pkg, err := importer.ForCompiler(token.NewFileSet(), "gc", lookup).Import(path)
	if err != nil {
		return err
	}
	seen := map[types.Type]bool{}
	for _, name := range pkg.Scope().Names() {
		object := pkg.Scope().Lookup(name)
		if object.Exported() {
			if err := checkType(object.Type(), seen); err != nil {
				return fmt.Errorf("public %s: %w", name, err)
			}
		}
	}
	return nil
}

// The list includes only the instruction and resource read services and their measured
// support dependencies. Compiler-classified standard library packages are
// allowed separately. Nothing is allowed by an internal directory prefix.
func allowedImplementation(p packageData) bool {
	if p.Standard {
		return true
	}
	switch p.ImportPath {
	case module + "/pkg/projmux/core",
		module + "/internal/services/agentcatalog",
		module + "/internal/integrations/agents/claude",
		module + "/internal/integrations/agents",
		module + "/internal/services/resources",
		module + "/internal/services/runtime",
		module + "/internal/core/runtimediag",
		module + "/internal/version",
		module + "/internal/core/agentprogress",
		module + "/internal/core/aicapability",
		module + "/internal/core/candidates",
		module + "/internal/core/layout",
		module + "/internal/core/lifecycle",
		module + "/internal/core/resources",
		module + "/internal/core/terminaltext",
		module + "/internal/integrations/agents/codexappserver",
		module + "/internal/integrations/agents/localipc",
		module + "/internal/integrations/hooks",
		module + "/internal/integrations/metadata",
		module + "/internal/integrations/mux",
		module + "/internal/integrations/procfsresources",
		module + "/internal/integrations/tmux",
		module + "/internal/integrations/tmuxexec",
		module + "/internal/integrations/tmuxopts",
		module + "/internal/theme",
		module + "/pkg/projmux",
		module + "/internal/services/instructions",
		module + "/internal/services/profiles",
		module + "/internal/core/metadata",
		module + "/internal/core/persona",
		module + "/internal/core/profile",
		module + "/internal/core/projectlinks",
		module + "/internal/core/registryview",
		module + "/internal/core/resourcegraph",
		module + "/internal/core/selector",
		module + "/internal/config",
		module + "/internal/state",
		module + "/internal/aiprovider",
		"golang.org/x/sys/unix", "golang.org/x/mod/semver":
		return true
	case "golang.org/x/term":
		// Project hook trust uses terminal detection; this is not CLI dispatch.
		return true
	}
	return false
}
func checkObject(object *types.TypeName) error {
	if object.Pkg() != nil && strings.Contains(object.Pkg().Path(), "/internal/") {
		return fmt.Errorf("internal type leaks from %s.%s", object.Pkg().Path(), object.Name())
	}
	return nil
}
func checkType(t types.Type, seen map[types.Type]bool) error {
	if t == nil || seen[t] {
		return nil
	}
	seen[t] = true
	visit := func(t types.Type) error { return checkType(t, seen) }
	switch t := t.(type) {
	case *types.Alias:
		if err := checkObject(t.Obj()); err != nil {
			return err
		}
		if err := visit(t.Rhs()); err != nil {
			return err
		}
		if err := checkParams(t.TypeParams(), seen); err != nil {
			return err
		}
		return checkArgs(t.TypeArgs(), seen)
	case *types.Named:
		if err := checkObject(t.Obj()); err != nil {
			return err
		}
		if err := checkParams(t.TypeParams(), seen); err != nil {
			return err
		}
		if err := checkArgs(t.TypeArgs(), seen); err != nil {
			return err
		}
		if err := visit(t.Underlying()); err != nil {
			return err
		}
		for method := range t.Methods() {
			if method.Exported() {
				if err := visit(method.Type()); err != nil {
					return err
				}
			}
		}
	case *types.Pointer:
		return visit(t.Elem())
	case *types.Slice:
		return visit(t.Elem())
	case *types.Array:
		return visit(t.Elem())
	case *types.Map:
		if err := visit(t.Key()); err != nil {
			return err
		}
		return visit(t.Elem())
	case *types.Chan:
		return visit(t.Elem())
	case *types.Struct:
		for field := range t.Fields() {
			if field.Exported() || field.Embedded() {
				if err := visit(field.Type()); err != nil {
					return err
				}
			}
		}
	case *types.Signature:
		if err := checkParams(t.TypeParams(), seen); err != nil {
			return err
		}
		if err := checkParams(t.RecvTypeParams(), seen); err != nil {
			return err
		}
		if err := visit(t.Params()); err != nil {
			return err
		}
		return visit(t.Results())
	case *types.Tuple:
		for v := range t.Variables() {
			if err := visit(v.Type()); err != nil {
				return err
			}
		}
	case *types.Interface:
		t.Complete()
		for method := range t.Methods() {
			if err := visit(method.Type()); err != nil {
				return err
			}
		}
		for etyp := range t.EmbeddedTypes() {
			if err := visit(etyp); err != nil {
				return err
			}
		}
	case *types.TypeParam:
		return visit(t.Constraint())
	case *types.Union:
		for term := range t.Terms() {
			if err := visit(term.Type()); err != nil {
				return err
			}
		}
	}
	return nil
}
func checkParams(params *types.TypeParamList, seen map[types.Type]bool) error {
	if params != nil {
		for tparam := range params.TypeParams() {
			if err := checkType(tparam, seen); err != nil {
				return err
			}
		}
	}
	return nil
}
func checkArgs(args *types.TypeList, seen map[types.Type]bool) error {
	if args != nil {
		for t := range args.Types() {
			if err := checkType(t, seen); err != nil {
				return err
			}
		}
	}
	return nil
}
