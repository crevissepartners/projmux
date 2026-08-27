package app

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

func closedRuntimeMutationVerbs() []runtimeMutationVerb {
	verbs := make([]runtimeMutationVerb, 0, len(runtimeMutationInventory))
	for verb := range runtimeMutationInventory {
		verbs = append(verbs, verb)
	}
	slices.Sort(verbs)
	return verbs
}

func printableRuntimeMutationInventory() runtimeMutationPlan {
	verbs := closedRuntimeMutationVerbs()
	actions := make([]plannedRuntimeMutation, 0, len(verbs))
	for i, verb := range verbs {
		authority := (&runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"}).printable()
		target := runtimeMutationTarget{
			Socket: "-L=phase10-inventory", PhysicalSocket: "/tmp/phase10-inventory",
			RouteAuthority: authority,
			Kind:           "session", ID: "$1", Parent: "$1/@1/root",
			UID: "uid:" + string(verb),
		}
		action := newRuntimeMutation(i+1, verb, target)
		switch verb {
		case mutationCreateSession:
			action.Target.PhysicalSocket, action.Target.Kind, action.Target.ID = runtimeMutationSocketAbsentBeforeCreate, "project-declaration", "inventory"
			action.Target.RouteAuthority = ""
			action.Operands = []string{"-f", "/tmp/projmux-inventory.conf", "-d", "-s", "inventory"}
		case mutationBootstrapControlSession:
			action.Target.PhysicalSocket, action.Target.Kind, action.Target.ID = runtimeMutationSocketAbsentBeforeCreate, "control-session-declaration", "declaration:-L=phase10-inventory/session=home"
			action.Target.RouteAuthority = ""
			action.Operands = []string{"-L", "phase10-inventory", "-f", "/tmp/config", "-d", "-s", "home"}
		case mutationWriteRouteMarker:
			action.Target.Socket, action.Target.Kind, action.Target.ID, action.Target.UID = "-S=/tmp/phase10-inventory", "app-server", "socket:/tmp/phase10-inventory", "logical:phase10-inventory"
			action.Operands = []string{"-S", "/tmp/phase10-inventory", "-gq", runtimeMutationSocketNameOption, "phase10-inventory"}
		case mutationCreateWindow:
			action.Operands = []string{"-d", "-t", "$1:"}
		case mutationCreatePane:
			action.Target.Kind, action.Target.ID = "pane", "%1"
			action.Operands = []string{"-d", "-t", "%1"}
		case mutationWriteLayout:
			action.Target.Kind, action.Target.ID = "pane", "%1"
			action.Operands = []string{"-t", "%1", "-x", "40"}
		case mutationKillPane, mutationTombstonePane, mutationRestorePane, mutationWriteIdentity, mutationWriteOption, mutationWritePresentationOption:
			action.Target.Kind, action.Target.ID = "pane", "%1"
			action.Operands = []string{"-t", "%1"}
			if verb == mutationTombstonePane || verb == mutationRestorePane {
				action.Operands = append(action.Operands, tmuxopts.PaneUID, action.Target.UID)
			} else if verb == mutationWriteIdentity || verb == mutationWriteOption || verb == mutationWritePresentationOption {
				field, class := tmuxopts.PaneUID, controllerRuntimeMutationManaged
				if verb == mutationWriteOption {
					field = tmuxopts.PaneName
				}
				if verb == mutationWritePresentationOption {
					field, class = aiPaneStateOption, controllerRuntimeMutationPresentation
				}
				action.Operands = []string{"-p", "-t", "%1", "-q", field, action.Target.UID}
				if verb == mutationWritePresentationOption {
					action.Operands = []string{"-p", "-t", "%1", field, action.Target.UID}
				}
				action.Target.Parent = "controller.identity/window_id=@1"
				action.Controller = &runtimeMutationControllerEffect{Class: string(class), Mode: controllerRuntimeMutationForward, Scope: string(resourcegraph.ObjectPane), Field: field, After: action.Target.UID}
			}
		case mutationKillWindow, mutationRenameWindow:
			action.Target.Kind, action.Target.ID = "window", "@1"
			action.Operands = []string{"-t", "@1"}
			if verb == mutationRenameWindow {
				action.Operands = append(action.Operands, "inventory")
			}
		case mutationWriteStableName:
			action.Target.Kind, action.Target.ID = "window", "@1"
			action.Operands = []string{"-w", "-t", "@1", "-q", tmuxopts.WindowName, "inventory"}
		case mutationQueuePaneKill:
			action.Target.Kind, action.Target.ID, action.Target.UID, action.Target.Parent = "pane", "%1", "deleted:pan-1", "$1/@1/root"
			action.Queue = &runtimeMutationQueuedKill{PhysicalSocket: action.Target.PhysicalSocket, LogicalSocket: "phase10-inventory", RouteAuthority: authority, ExpectedUID: action.Target.UID, SessionID: "$1", WindowID: "@1"}
			action.Queue.Marker = runtimeMutationQueueMarker(action)
		case mutationQueueWindowKill:
			action.Target.Kind, action.Target.ID, action.Target.UID, action.Target.Parent = "window", "@1", "deleted:win-1", "$1/root"
			action.Queue = &runtimeMutationQueuedKill{PhysicalSocket: action.Target.PhysicalSocket, LogicalSocket: "phase10-inventory", RouteAuthority: authority, ExpectedUID: action.Target.UID, SessionID: "$1"}
			action.Queue.Marker = runtimeMutationQueueMarker(action)
		case mutationFinalizeSession:
			action.Operands = []string{"-t", "$1", finalizeOperationEnvironment, "op"}
		case mutationWriteLease:
			action.Operands = []string{"-t", "$1", createOperationEnvironment, "op"}
		case mutationClearLease:
			action.Operands = []string{"-u", "-t", "$1", createOperationEnvironment}
		case mutationWriteProjectAnchor:
			action.Operands = []string{"-t", "$1", inttmux.ProjectPathSessionOption, "/work"}
		case mutationStopManagedSession, mutationStopUnmanagedSession, mutationKillOwned:
			action.Operands = []string{"-t", "$1"}
		}
		bindRuntimeMutationGuard(&action, "inventory")
		actions = append(actions, action)
	}
	return newRuntimeMutationPlan(actions...)
}

func TestPlanOnlyMutationInventoryIsClosedAndPrintable(t *testing.T) {
	want := []runtimeMutationVerb{
		mutationBootstrapControlSession,
		mutationClearLease,
		mutationConvergeControlIdentity,
		mutationCreatePane,
		mutationCreateSession,
		mutationCreateWindow,
		mutationFinalizeSession,
		mutationKillOwned,
		mutationKillPane,
		mutationKillWindow,
		mutationQueuePaneKill,
		mutationQueueWindowKill,
		mutationRenameWindow,
		mutationRestorePane,
		mutationTombstonePane,
		mutationStopManagedSession,
		mutationStopUnmanagedSession,
		mutationWriteIdentity,
		mutationWriteOption,
		mutationWritePresentationOption,
		mutationWriteLayout,
		mutationWriteLease,
		mutationWriteProjectAnchor,
		mutationWriteRouteMarker,
		mutationWriteStableName,
	}
	slices.Sort(want)
	if got := closedRuntimeMutationVerbs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime mutation vocabulary = %v, want closed inventory %v", got, want)
	}
	plan := printableRuntimeMutationInventory()
	first, err := plan.printableBytes()
	if err != nil {
		t.Fatalf("print inventory: %v", err)
	}
	second, err := newRuntimeMutationPlan(slices.Clone(plan.Actions)...).printableBytes()
	if err != nil {
		t.Fatalf("repeat print inventory: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("inventory bytes changed:\n%s\n---\n%s", first, second)
	}
	for _, action := range plan.Actions {
		if action.Verb == "" || action.Target.Socket == "" || action.Target.PhysicalSocket == "" || action.Target.Kind == "" ||
			(action.Target.ID == "" && action.Target.UID == "") || action.Guard.Kind == "" || action.Guard.Expect == "" || action.Order == 0 || action.Effect == "" {
			t.Fatalf("incomplete printable inventory row: %#v", action)
		}
		if !strings.Contains(action.Guard.Expect, action.Target.UID) {
			t.Fatalf("guard is not bound to stable target uid: %#v", action)
		}
	}
}

func TestPlanOnlyMutationProductSurfaceInventoryIsBidirectionalAndClosed(t *testing.T) {
	seen := map[string]bool{}
	byID := map[string]runtimeMutationSurface{}
	for _, row := range runtimeMutationSurfaces {
		if row.ID == "" || row.Producer == "" || row.Handler == "" || row.SemanticClass == "" ||
			row.RootKinds == "" || row.OwnerRoute == "" || row.PlanVerb == "" || row.Guard == "" || row.Effect == "" {
			t.Fatalf("incomplete product mutation surface: %#v", row)
		}
		if seen[row.ID] {
			t.Fatalf("duplicate product mutation surface %q", row.ID)
		}
		seen[row.ID] = true
		byID[row.ID] = row
		if row.Disposition != runtimeMutationSurfacePlanned && row.Disposition != runtimeMutationSurfaceExempt {
			t.Fatalf("surface %q has open disposition %q", row.ID, row.Disposition)
		}
	}
	for _, required := range []string{
		"project.materialize", "project.bootstrap-route-marker", "control.bootstrap", "controller.identity", "controller.option", "agent.presentation", "codex.native-lifecycle-authority", "pane.canonical-delete", "window.canonical-delete",
		"public.create-window", "public.create-pane", "project.delete-cascade-pane", "project.delete-cascade-window", "layout.auto-even-split",
		"startup.shell-project", "startup.sidebar-project", "startup.current-project", "startup.attach-project", "startup.session-picker-project",
		"pane-menu.split-right", "pane-menu.split-down", "pane-menu.kill", "pane-menu.resume", "pane-menu.swap-up",
		"pane-menu.swap-down", "pane-menu.mark", "pane-menu.zoom", "pane-menu.mouse-forward", "shell.foreground-attach",
		"app.quit", "attach.ensure-home", "attach.ephemeral-prune", "attach.ephemeral-create", "standalone.prune", "manual.tagged-kill", "switch.manual-kill", "sidebar.unmanaged-candidate-stop", "replay.retired-snapshot",
		"config.apply-source", "pane.rebalance", "trigger.after-new-window", "trigger.after-split-window",
		"trigger.after-kill-pane", "trigger.after-kill-pane.rebalance", "trigger.pane-exited", "trigger.pane-exited.rebalance", "trigger.pane-died", "trigger.pane-died.rebalance", "trigger.window-unlinked",
		"trigger.attention-focus", "trigger.recent-window-record", "trigger.client-attached-welcome", "config.generated-statusbar", "config.generated-key-sequences",
		"resource.rename-project", "resource.rename-window", "resource.rename-pane", "resource.rebind-project",
		"settings.desktop-notify-option", "settings.statusbar-decoration-option", "settings.ai-badge-option",
		"settings.generated-config-reload", "settings.key-sequence-retirement", "ai.integrate-tmux-bell", "notification.wsl-legacy-cleanup-marker",
	} {
		if !seen[required] {
			t.Errorf("closed product inventory is missing %q", required)
		}
	}
	marker := byID["project.bootstrap-route-marker"]
	if marker.Handler != "materializer.writeCreatedProjectRouteMarker" || marker.PlanVerb != string(mutationWriteRouteMarker) {
		t.Fatalf("fresh Project route-marker surface does not map bidirectionally to its typed producer: %#v", marker)
	}
	presentation := byID["agent.presentation"]
	if presentation.Disposition != runtimeMutationSurfaceExempt || presentation.PlanVerb != string(mutationWritePresentationOption) ||
		!strings.Contains(presentation.Guard, "closed topic/manual-topic/state/badge/attention") {
		t.Fatalf("Agent presentation writes are not exactly classified as a typed exemption: %#v", presentation)
	}
	codexAuthority := byID["codex.native-lifecycle-authority"]
	if codexAuthority.Disposition != runtimeMutationSurfaceExempt ||
		!strings.Contains(codexAuthority.Guard, "authority, epoch, and bounded reason options only") ||
		!strings.Contains(codexAuthority.OwnerRoute, "exact Registry Agent/Pane activation") {
		t.Fatalf("Codex lifecycle authority metadata is not exactly classified: %#v", codexAuthority)
	}
	managedOptions := byID["controller.option"]
	if managedOptions.Disposition != runtimeMutationSurfacePlanned || managedOptions.PlanVerb != string(mutationWriteOption) ||
		strings.Contains(managedOptions.Effect, "topic") || strings.Contains(managedOptions.Effect, "badge") {
		t.Fatalf("managed controller option surface absorbed presentation fields: %#v", managedOptions)
	}
}

func TestControllerAdapterFieldInventoryIsSourceDerivedAndBidirectional(t *testing.T) {
	targets := map[string]map[string]bool{
		"../integrations/metadata/tmuxmirror.go": {
			"MirrorProject": false, "RebindProject": false, "MirrorWindow": false,
			"disableAutomaticRename": false, "writeWindowIdentityName": false, "writeWindowDisplayName": false,
			"MirrorPane": false, "writePaneName": false,
		},
		"resource_reconcile_plan.go": {
			"planResourceAgentProjections": false, "planAuthorshipPromotionOptions": false, "recordWrite": false,
		},
		"resource_controller.go": {"controllerRecoveryCandidates": false},
	}
	selectorFields := map[string]string{
		"ProjectUIDSession": tmuxopts.ProjectUIDSession, "ProjectNameSession": tmuxopts.ProjectNameSession,
		"ProjectPathSession": tmuxopts.ProjectPathSession, "WindowUID": tmuxopts.WindowUID,
		"AutomaticRenameWindow": tmuxopts.AutomaticRenameWindow, "WindowName": tmuxopts.WindowName,
		"PaneUID": tmuxopts.PaneUID, "PaneName": tmuxopts.PaneName,
		"AgentSessionIDPane": tmuxopts.AgentSessionIDPane, "AgentThreadIDPane": tmuxopts.AgentThreadIDPane,
		"PaneOwnerKind": tmuxopts.PaneOwnerKind, "PaneOwnerUID": tmuxopts.PaneOwnerUID,
		"PaneRole": tmuxopts.PaneRole, "AgentUIDPane": tmuxopts.AgentUIDPane,
		"AgentProviderPane": tmuxopts.AgentProviderPane,
	}
	identFields := map[string]string{
		"aiPaneTopicOption": aiPaneTopicOption, "aiPaneTopicManualOption": aiPaneTopicManualOption,
		"aiPaneStateOption": aiPaneStateOption, "aiPaneBadgeKindOption": aiPaneBadgeKindOption,
		"attentionStateOption": attentionStateOption,
	}
	discovered := map[string]bool{}
	for path, functions := range targets {
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse controller producer %s: %v", path, err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if _, tracked := functions[function.Name.Name]; !tracked {
				continue
			}
			functions[function.Name.Name] = true
			ast.Inspect(function.Body, func(node ast.Node) bool {
				switch value := node.(type) {
				case *ast.SelectorExpr:
					if pkg, ok := value.X.(*ast.Ident); ok && pkg.Name == "tmuxopts" {
						if field := selectorFields[value.Sel.Name]; field != "" {
							discovered[field] = true
						}
					}
				case *ast.Ident:
					if field := identFields[value.Name]; field != "" {
						discovered[field] = true
					}
				case *ast.BasicLit:
					if value.Kind == token.STRING {
						literal, _ := strconv.Unquote(value.Value)
						if literal == "window_name" {
							discovered[literal] = true
						}
					}
				}
				return true
			})
		}
		for function, found := range functions {
			if !found {
				t.Errorf("controller producer %s:%s is absent", path, function)
			}
		}
	}
	wantManaged := map[string]bool{}
	for _, fields := range controllerRuntimeMutationManagedFields {
		for _, field := range fields {
			wantManaged[field] = true
		}
	}
	wantPresentation := map[string]bool{}
	for _, field := range controllerRuntimeMutationPresentationFields {
		wantPresentation[field] = true
	}
	if len(wantPresentation) != 5 {
		t.Fatalf("presentation field inventory = %v, want exactly five", controllerRuntimeMutationPresentationFields)
	}
	if wantManaged[tmuxopts.SessionRole] || discovered[tmuxopts.SessionRole] {
		t.Fatal("ControlSession role leaked into the controller adapter; converge-control-identity owns it")
	}
	if len(discovered) != len(wantManaged)+len(wantPresentation) {
		t.Fatalf("source-derived controller fields = %v, managed=%v presentation=%v", discovered, wantManaged, wantPresentation)
	}
	for field := range discovered {
		class, ok := controllerRuntimeMutationFieldClassFor(func() string {
			switch {
			case slices.Contains(controllerRuntimeMutationManagedFields["session"], field):
				return "session"
			case slices.Contains(controllerRuntimeMutationManagedFields["window"], field):
				return "window"
			default:
				return "pane"
			}
		}(), field)
		if !ok || (wantManaged[field] && class != controllerRuntimeMutationManaged) || (wantPresentation[field] && class != controllerRuntimeMutationPresentation) {
			t.Errorf("source field %s has class=%q ok=%v", field, class, ok)
		}
		delete(wantManaged, field)
		delete(wantPresentation, field)
	}
	if len(wantManaged) != 0 || len(wantPresentation) != 0 {
		t.Fatalf("synthetic controller grants without a producer: managed=%v presentation=%v", wantManaged, wantPresentation)
	}
}

func TestGeneratedCatalogMutationAndNavigationArtifactsHaveOneSurfaceRow(t *testing.T) {
	rows := map[string][]runtimeMutationSurface{}
	for _, row := range runtimeMutationSurfaces {
		if after, ok := strings.CutPrefix(row.ID, "catalog."); ok {
			rows[after] = append(rows[after], row)
		}
	}
	seenCanonical := map[string]string{}
	seenLegacy := map[string]string{}
	for _, action := range defaultKeyBindingCatalog() {
		if strings.TrimSpace(action.CanonicalID) == "" {
			t.Errorf("generated catalog artifact %q has no public canonical id", action.ID)
			continue
		}
		if prior := seenCanonical[action.CanonicalID]; prior != "" {
			t.Errorf("generated catalog canonical id %q is shared by legacy ids %q and %q", action.CanonicalID, prior, action.ID)
		}
		seenCanonical[action.CanonicalID] = action.ID
		if prior := seenLegacy[action.ID]; prior != "" {
			t.Errorf("generated catalog legacy id %q is shared by canonical ids %q and %q", action.ID, prior, action.CanonicalID)
		}
		seenLegacy[action.ID] = action.CanonicalID
		matched := rows[action.CanonicalID]
		if len(matched) != 1 {
			t.Errorf("generated catalog artifact %q canonical=%q has %d product-surface rows, want one", action.ID, action.CanonicalID, len(matched))
		} else if matched[0].LegacyID != action.ID {
			t.Errorf("catalog surface %q legacy alias = %q, want shipped id %q", matched[0].ID, matched[0].LegacyID, action.ID)
		}
		for field := range strings.FieldsSeq(action.TmuxBody + " " + strings.Join(action.TmuxBodyAliases, " ")) {
			field = strings.Trim(field, `{};'"`)
			if closedTmuxTopologyMutationVerbs[field] {
				t.Errorf("generated catalog artifact %q embeds managed topology verb %q instead of its classified handler", action.ID, field)
			}
		}
		delete(rows, action.CanonicalID)
	}
	for id, unmatched := range rows {
		t.Errorf("surface row catalog.%s x%d has no generated catalog artifact", id, len(unmatched))
	}
}

func resolveNativeProjectionString(expr ast.Expr, constants map[string]string) (string, bool) {
	switch value := expr.(type) {
	case *ast.BasicLit:
		if value.Kind != token.STRING {
			return "", false
		}
		resolved, err := strconv.Unquote(value.Value)
		return resolved, err == nil
	case *ast.Ident:
		resolved, ok := constants[value.Name]
		return resolved, ok
	case *ast.BinaryExpr:
		if value.Op != token.ADD {
			return "", false
		}
		left, leftOK := resolveNativeProjectionString(value.X, constants)
		right, rightOK := resolveNativeProjectionString(value.Y, constants)
		return left + right, leftOK && rightOK
	default:
		return "", false
	}
}

func nativeProjectionStringConstants(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	pending := map[string]ast.Expr{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parse native projection constant source %s: %v", entry.Name(), err)
		}
		for _, declaration := range parsed.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, spec := range general.Specs {
				values := spec.(*ast.ValueSpec)
				for index, name := range values.Names {
					if index < len(values.Values) {
						pending[name.Name] = values.Values[index]
					}
				}
			}
		}
	}
	constants := map[string]string{"aiPaneLaunchAuthorshipOption": tmuxopts.AgentLaunchAuthorshipPane}
	for changed := true; changed; {
		changed = false
		for name, expr := range pending {
			if value, ok := resolveNativeProjectionString(expr, constants); ok {
				constants[name] = value
				delete(pending, name)
				changed = true
			}
		}
	}
	return constants
}

func nativeProjectionOptionOperand(args []ast.Expr, constants map[string]string) (ast.Expr, error) {
	for index := 0; index < len(args); index++ {
		value, resolved := resolveNativeProjectionString(args[index], constants)
		if resolved && strings.HasPrefix(value, "-") {
			if value == "-t" {
				index++
				if index >= len(args) {
					return nil, errors.New("set-option target flag has no target")
				}
			}
			continue
		}
		return args[index], nil
	}
	return nil, errors.New("set-option argv has no option operand")
}

func nativeProjectionStructOptionIndex(structure *ast.StructType) (int, bool) {
	index := 0
	for _, field := range structure.Fields.List {
		if len(field.Names) == 0 {
			index++
			continue
		}
		for _, name := range field.Names {
			if name.Name == "option" {
				return index, true
			}
			index++
		}
	}
	return 0, false
}

func extractNativeAgentProjectionOptions(function *ast.FuncDecl, constants map[string]string) (map[string]bool, error) {
	options := map[string]bool{}
	var failures []error
	rowOptionReference := false
	rowInventory := false
	addOperand := func(expr ast.Expr) {
		if selector, ok := expr.(*ast.SelectorExpr); ok && selector.Sel.Name == "option" {
			rowOptionReference = true
			return
		}
		value, ok := resolveNativeProjectionString(expr, constants)
		if !ok || !strings.HasPrefix(value, "@") {
			if identifier, ok := expr.(*ast.Ident); ok {
				failures = append(failures, fmt.Errorf("unresolved or non-option set-option operand identifier %s", identifier.Name))
			} else {
				failures = append(failures, fmt.Errorf("unresolved or non-option set-option operand %T", expr))
			}
			return
		}
		options[value] = true
	}
	ast.Inspect(function.Body, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.CompositeLit:
			array, ok := value.Type.(*ast.ArrayType)
			if !ok {
				return true
			}
			if structure, ok := array.Elt.(*ast.StructType); ok {
				optionIndex, hasOption := nativeProjectionStructOptionIndex(structure)
				if !hasOption {
					return true
				}
				rowInventory = true
				for _, element := range value.Elts {
					row, ok := element.(*ast.CompositeLit)
					if !ok {
						failures = append(failures, errors.New("option row is not a closed composite literal"))
						continue
					}
					var operand ast.Expr
					for index, field := range row.Elts {
						if keyed, ok := field.(*ast.KeyValueExpr); ok {
							if key, ok := keyed.Key.(*ast.Ident); ok && key.Name == "option" {
								operand = keyed.Value
							}
						} else if index == optionIndex {
							operand = field
						}
					}
					if operand == nil {
						failures = append(failures, errors.New("option row has no option operand"))
						continue
					}
					addOperand(operand)
				}
				return true
			}
			if identifier, ok := array.Elt.(*ast.Ident); ok && identifier.Name == "string" && len(value.Elts) > 0 {
				if first, ok := resolveNativeProjectionString(value.Elts[0], constants); ok && first == "set-option" {
					operand, err := nativeProjectionOptionOperand(value.Elts[1:], constants)
					if err != nil {
						failures = append(failures, err)
					} else {
						addOperand(operand)
					}
				}
			}
		case *ast.CallExpr:
			for index := 0; index+1 < len(value.Args); index++ {
				first, firstOK := resolveNativeProjectionString(value.Args[index], constants)
				second, secondOK := resolveNativeProjectionString(value.Args[index+1], constants)
				if !firstOK || !secondOK || first != "tmux" || second != "set-option" {
					continue
				}
				operand, err := nativeProjectionOptionOperand(value.Args[index+2:], constants)
				if err != nil {
					failures = append(failures, err)
				} else {
					addOperand(operand)
				}
				break
			}
		}
		return true
	})
	if rowOptionReference && !rowInventory {
		failures = append(failures, errors.New("dynamic field.option operand has no closed option row inventory"))
	}
	return options, errors.Join(failures...)
}

func TestNativeAgentProjectionWriterFieldInventoryIsClosed(t *testing.T) {
	targets := map[string]map[string][]string{
		"ai.go": {
			"BindAgentPaneOnRoute": {
				aiPaneManagedOption, aiPaneAgentOption, aiPaneLaunchAuthorshipOption, aiPaneContextOption,
				aiPaneTopicOption, aiPaneTopicManualOption, aiPaneStateOption, aiPaneSessionIDOption,
				aiPaneResumeIDOption, aiPaneResumeSourceOption, aiPaneResumeUpdatedAtOption, aiPaneThreadIDOption,
				aiPaneCodexAuthorityOption, aiPaneCodexEpochOption, aiPaneCodexReasonOption,
			},
		},
		"agent_interaction.go": {
			"WriteTopic":       {aiPaneTopicOption, aiPaneTopicManualOption},
			"WriteInteraction": {aiPaneStateOption, aiPaneBadgeKindOption, attentionStateOption},
		},
		"ai_ingest_codex.go": {
			"applyCodexHookSemanticDelivery": {aiPaneStateOption, aiPaneBadgeKindOption, attentionStateOption},
		},
		"ai_ingest_codex_native.go": {
			"SetAuthority":  {aiPaneCodexAuthorityOption, aiPaneCodexEpochOption, aiPaneCodexReasonOption},
			"Apply":         {aiPaneStateOption, aiPaneBadgeKindOption, attentionStateOption},
			"ApplyProgress": {aiPaneCodexDroppedOption, aiPaneCodexUnknownOption, aiPaneCodexOverflowOption},
		},
	}
	constants := nativeProjectionStringConstants(t)
	for path, functions := range targets {
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse native Agent projection producer %s: %v", path, err)
		}
		found := map[string]bool{}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			want, tracked := functions[function.Name.Name]
			if !tracked {
				continue
			}
			found[function.Name.Name] = true
			got, err := extractNativeAgentProjectionOptions(function, constants)
			if err != nil {
				t.Errorf("native Agent projection %s:%s is not closed: %v", path, function.Name.Name, err)
				continue
			}
			slices.Sort(want)
			gotFields := make([]string, 0, len(got))
			for option := range got {
				gotFields = append(gotFields, option)
			}
			slices.Sort(gotFields)
			if !slices.Equal(gotFields, want) {
				t.Errorf("native Agent projection %s:%s fields=%v, want %v", path, function.Name.Name, gotFields, want)
			}
		}
		for function := range functions {
			if !found[function] {
				t.Errorf("native Agent projection producer %s:%s is absent", path, function)
			}
		}
	}
}

func TestNativeAgentProjectionOptionExtractorRejectsInventoryExpansionAndDynamicOperands(t *testing.T) {
	constants := map[string]string{"knownOption": "@known", "extraOption": "@extra"}
	for _, test := range []struct {
		name      string
		source    string
		wantExtra bool
		wantError bool
	}{
		{"extra identifier", `package app; func mutate() { for _, field := range []struct{ option, value string }{{knownOption, "x"}, {extraOption, "y"}} { _ = []string{"set-option", "-p", "-t", "%1", field.option, field.value} } }`, true, false},
		{"extra literal", `package app; func mutate() { run("tmux", "set-option", "-p", "-t", "%1", "@extra", "x") }`, true, false},
		{"dynamic operand", `package app; func mutate(option string) { run("tmux", "set-option", "-p", "-t", "%1", option, "x") }`, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := parser.ParseFile(token.NewFileSet(), "mutation.go", test.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			function := parsed.Decls[0].(*ast.FuncDecl)
			got, err := extractNativeAgentProjectionOptions(function, constants)
			if (err != nil) != test.wantError {
				t.Fatalf("extract error = %v, want error=%v", err, test.wantError)
			}
			if got["@extra"] != test.wantExtra {
				t.Fatalf("extra option detected=%v, want %v (fields=%v)", got["@extra"], test.wantExtra, got)
			}
		})
	}
}

func TestCanonicalCreateProducerInventoryMapsEveryNativeAndGeneratedSurface(t *testing.T) {
	rowsByID := map[string]bool{}
	for _, row := range runtimeMutationSurfaces {
		rowsByID[row.ID] = true
	}
	want := map[canonicalCreateProducer][]string{
		canonicalProducerPaneMenu:       {"pane-menu.split-right", "pane-menu.split-down"},
		canonicalProducerSavedDefault:   {"catalog.agent-pane.launch-default.right", "catalog.agent-pane.launch-default.down"},
		canonicalProducerProviderPicker: {"native-picker.provider-create"},
		canonicalProducerResumePicker:   {"native-picker.resume-create"},
		canonicalProducerDirectProvider: {"catalog.agent.create.codex.right", "catalog.agent.create.codex.down", "catalog.agent.create.claude.right", "catalog.agent.create.claude.down"},
		canonicalProducerDirectShell:    {"catalog.pane.create.shell.right", "catalog.pane.create.shell.down"},
	}
	if len(want) != len(canonicalCreateProducers) {
		t.Fatalf("canonical producer surface table has %d rows, source inventory has %d: %v", len(want), len(canonicalCreateProducers), canonicalCreateProducers)
	}
	for _, producer := range canonicalCreateProducers {
		ids := want[producer]
		if len(ids) == 0 {
			t.Errorf("canonical create producer %q has no product-surface mapping", producer)
			continue
		}
		for _, id := range ids {
			if !rowsByID[id] {
				t.Errorf("canonical create producer %q maps missing product-surface row %q", producer, id)
			}
		}
	}
	windowWant := map[canonicalCreateProducer]string{
		canonicalProducerWindowCreate: "catalog.window.create",
		canonicalProducerWindowRename: "catalog.window.rename",
	}
	if len(windowWant) != len(canonicalWindowMutationProducers) {
		t.Fatalf("canonical Window producer surface table has %d rows, source inventory has %d: %v", len(windowWant), len(canonicalWindowMutationProducers), canonicalWindowMutationProducers)
	}
	for _, producer := range canonicalWindowMutationProducers {
		id := windowWant[producer]
		if id == "" || !rowsByID[id] {
			t.Errorf("canonical Window producer %q maps missing product-surface row %q", producer, id)
		}
	}
}

func TestGeneratedWindowLifecycleActionsReachTypedHandlersWithoutRawManagedVerbs(t *testing.T) {
	wantRoute := map[string]string{
		"new-window":    "internal tmux window-create --client #{client_tty} --anchor #{pane_id}",
		"rename-window": "internal tmux window-rename --client #{client_tty} --anchor #{pane_id}",
	}
	seen := map[string]bool{}
	for _, action := range defaultKeyBindingCatalog() {
		route, ok := wantRoute[action.ID]
		if !ok {
			continue
		}
		seen[action.ID] = true
		wantCanonical := "window.create"
		if action.ID == "rename-window" {
			wantCanonical = "window.rename"
		}
		if action.CanonicalID != wantCanonical {
			t.Fatalf("generated %s canonical id = %q, want %q", action.ID, action.CanonicalID, wantCanonical)
		}
		body := renderTmuxBindingBody("/usr/local/bin/projmux", action)
		if !strings.Contains(body, route) {
			t.Fatalf("generated %s body = %q, want typed handler %q", action.ID, body, route)
		}
		for _, raw := range []string{" new-window ", " rename-window ", " split-window ", " kill-session ", " kill-window ", " kill-pane "} {
			if strings.Contains(" "+body+" ", raw) {
				t.Fatalf("generated %s body bypasses typed handler with %q: %s", action.ID, raw, body)
			}
		}
	}
	for id := range wantRoute {
		if !seen[id] {
			t.Fatalf("generated lifecycle producer %q disappeared without product inventory review", id)
		}
	}
}

func TestGeneratedPaneMenuArtifactsHaveOneClosedSurfaceRow(t *testing.T) {
	rendered := strings.Join(tmuxPaneContextMenuBindings("/usr/local/bin/projmux"), "\n")
	managed := regexp.MustCompile(`pane-menu --client #\{client_tty\} ([a-z-]+) #\{pane_id\}`).FindAllStringSubmatch(rendered, -1)
	got := map[string]bool{"pane-menu.resume": strings.Contains(rendered, "ai-split-resume-right"),
		"pane-menu.swap-up": strings.Contains(rendered, "swap-pane -U"), "pane-menu.swap-down": strings.Contains(rendered, "swap-pane -D"),
		"pane-menu.mark": strings.Contains(rendered, "select-pane -m"), "pane-menu.zoom": strings.Contains(rendered, "resize-pane -Z"),
		"pane-menu.mouse-forward": strings.Contains(rendered, "send-keys -M")}
	for _, match := range managed {
		got["pane-menu."+match[1]] = true
	}
	want := []string{"pane-menu.resume", "pane-menu.split-right", "pane-menu.split-down", "pane-menu.kill", "pane-menu.swap-up", "pane-menu.swap-down", "pane-menu.mark", "pane-menu.zoom", "pane-menu.mouse-forward"}
	rows := map[string]int{}
	for _, row := range runtimeMutationSurfaces {
		if strings.HasPrefix(row.ID, "pane-menu.") {
			rows[row.ID]++
		}
	}
	for _, id := range want {
		if !got[id] || rows[id] != 1 {
			t.Errorf("generated menu surface %q: artifact=%t rows=%d, want true/1", id, got[id], rows[id])
		}
		delete(got, id)
		delete(rows, id)
	}
	if len(got) != 0 || len(rows) != 0 {
		t.Fatalf("unclassified generated/menu surface delta: artifacts=%v rows=%v", got, rows)
	}
	for verb := range closedTmuxTopologyMutationVerbs {
		if verb == "swap-pane" || verb == "resize-pane" {
			continue // exact presentation rows above
		}
		if strings.Contains(" "+rendered+" ", " "+verb+" ") {
			t.Fatalf("generated Pane menu contains unmanaged lifecycle verb %q", verb)
		}
	}
}

func TestFullRenderedTmuxConfigsHaveClosedGeneratedMutationSurfaces(t *testing.T) {
	rows := map[string]runtimeMutationSurfaceDisposition{}
	for _, row := range runtimeMutationSurfaces {
		rows[row.ID] = row.Disposition
	}
	for _, id := range []string{
		"trigger.attention-focus", "pane.rebalance", "trigger.pane-exited", "trigger.pane-exited.rebalance", "trigger.pane-died", "trigger.pane-died.rebalance",
		"trigger.after-kill-pane", "trigger.after-kill-pane.rebalance", "trigger.window-unlinked",
		"trigger.recent-window-record", "trigger.after-new-window", "trigger.after-split-window",
		"trigger.client-attached-welcome", "config.generated-statusbar", "config.generated-key-sequences",
		"pane-menu.swap-up", "pane-menu.swap-down", "pane-menu.zoom",
	} {
		if rows[id] == "" {
			t.Fatalf("generated config producer %q has no closed surface row", id)
		}
	}

	overridden, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), keymapFile{Bindings: map[string]keymapOverride{
		"window.create": {KeysSet: true, Keys: []string{"M-N"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defaultWindow, _ := keyBindingActionByID(defaultKeyBindingCatalog(), "new-window")
	overriddenWindow, _ := keyBindingActionByID(overridden, "new-window")
	if overriddenWindow.TmuxKind != defaultWindow.TmuxKind || overriddenWindow.TmuxBody != defaultWindow.TmuxBody ||
		!reflect.DeepEqual(overriddenWindow.TmuxBodyAliases, defaultWindow.TmuxBodyAliases) {
		t.Fatal("Settings key override changed the closed generated action handler")
	}
	decorations := statusbarDecorationSetFromGlobal(config.StatusbarDecorationOff)
	configs := map[string]string{
		"standalone":                   tmuxStandaloneConfig("/usr/local/bin/projmux", config.StatusbarDecorationOff),
		"app":                          tmuxAppConfig("/usr/local/bin/projmux", "/bin/sh", config.StatusbarDecorationOff),
		"standalone-settings-override": tmuxStandaloneConfigWithKeymap("/usr/local/bin/projmux", decorations, overridden, true),
		"app-settings-override":        tmuxAppConfigWithKeymap("/usr/local/bin/projmux", "/bin/sh", decorations, overridden, true),
	}
	requiredArtifacts := map[string]string{
		"trigger.attention-focus":        "set-hook -g pane-focus-out",
		"trigger.pane-exited":            "set-hook -g pane-exited",
		"trigger.pane-died":              "set-hook -g pane-died",
		"trigger.after-kill-pane":        "set-hook -g after-kill-pane",
		"trigger.window-unlinked":        "set-hook -g window-unlinked",
		"trigger.recent-window-record":   "set-hook -g after-select-window",
		"config.generated-statusbar":     "set -g status-right",
		"config.generated-key-sequences": tmuxSequenceRootsOption,
		"pane-menu.swap-up":              "swap-pane -U",
		"pane-menu.swap-down":            "swap-pane -D",
		"pane-menu.zoom":                 "resize-pane -Z",
	}
	appOnlyArtifacts := map[string]string{
		"trigger.after-new-window":        "set-hook -g after-new-window",
		"trigger.after-split-window":      "set-hook -g after-split-window",
		"trigger.client-attached-welcome": "set-hook -g client-attached",
	}
	expectedArtifactCounts := map[string]map[string]int{
		"standalone": {
			"trigger.attention-focus": 1, "trigger.pane-exited": 1, "trigger.pane-died": 1, "trigger.after-kill-pane": 1,
			"trigger.window-unlinked": 1, "trigger.recent-window-record": 1,
			"config.generated-statusbar": 2, "config.generated-key-sequences": 1,
			"pane-menu.swap-up": 1, "pane-menu.swap-down": 1, "pane-menu.zoom": 1,
		},
		"app": {
			"trigger.attention-focus": 1, "trigger.pane-exited": 1, "trigger.pane-died": 1, "trigger.after-kill-pane": 1,
			"trigger.window-unlinked": 1, "trigger.recent-window-record": 1,
			"config.generated-statusbar": 4, "config.generated-key-sequences": 2,
			"pane-menu.swap-up": 1, "pane-menu.swap-down": 1, "pane-menu.zoom": 1,
		},
		"standalone-settings-override": {
			"trigger.attention-focus": 1, "trigger.pane-exited": 1, "trigger.pane-died": 1, "trigger.after-kill-pane": 1,
			"trigger.window-unlinked": 1, "trigger.recent-window-record": 1,
			"config.generated-statusbar": 2, "config.generated-key-sequences": 1,
			"pane-menu.swap-up": 1, "pane-menu.swap-down": 1, "pane-menu.zoom": 1,
		},
		"app-settings-override": {
			"trigger.attention-focus": 1, "trigger.pane-exited": 1, "trigger.pane-died": 1, "trigger.after-kill-pane": 1,
			"trigger.window-unlinked": 1, "trigger.recent-window-record": 1,
			"config.generated-statusbar": 4, "config.generated-key-sequences": 2,
			"pane-menu.swap-up": 1, "pane-menu.swap-down": 1, "pane-menu.zoom": 1,
		},
	}
	for kind, rendered := range configs {
		wantConverge := 4
		if strings.HasPrefix(kind, "app") {
			wantConverge = 6
		}
		convergeCount := strings.Count(rendered, "internal tmux converge")
		unsetCount := strings.Count(rendered, "env -u TMUX -u TMUX_PANE '/usr/local/bin/projmux' internal tmux converge")
		if convergeCount != wantConverge || unsetCount != convergeCount {
			t.Errorf("%s generated config has converge=%d direct-env-unset=%d, want every one of %d controller hooks to discard incomplete ambient authority", kind, convergeCount, unsetCount, wantConverge)
		}
		for id, signature := range requiredArtifacts {
			want := expectedArtifactCounts[kind][id]
			if got := strings.Count(rendered, signature); got != want {
				t.Errorf("%s generated artifact %q signature %q count=%d, want exact %d", kind, id, signature, got, want)
			}
		}
		for id, signature := range appOnlyArtifacts {
			want := 0
			if strings.HasPrefix(kind, "app") {
				want = 1
			}
			if got := strings.Count(rendered, signature); got != want {
				t.Errorf("%s generated artifact %q signature %q count=%d, want %d", kind, id, signature, got, want)
			}
		}
		for hook, rebalanceID := range map[string]string{
			"pane-exited":     "trigger.pane-exited.rebalance",
			"pane-died":       "trigger.pane-died.rebalance",
			"after-kill-pane": "trigger.after-kill-pane.rebalance",
		} {
			prefix := "set-hook -g " + hook + " "
			var hookLines []string
			for line := range strings.SplitSeq(rendered, "\n") {
				if strings.HasPrefix(line, prefix) {
					hookLines = append(hookLines, line)
				}
			}
			if len(hookLines) != 1 || strings.Count(hookLines[0], "internal tmux rebalance-panes") != 1 {
				t.Errorf("%s generated %s hook does not map exactly once to %s: %v", kind, hook, rebalanceID, hookLines)
			}
			matchedRows := 0
			for id, disposition := range rows {
				if id == "trigger."+hook || id == rebalanceID {
					matchedRows++
					if disposition != runtimeMutationSurfaceExempt {
						t.Errorf("generated %s hook surface %q disposition=%q, want presentation/observation exemption", hook, id, disposition)
					}
				}
			}
			if matchedRows != 2 {
				t.Errorf("generated %s hook maps %d surface rows, want exact controller+rebalance pair", hook, matchedRows)
			}
		}
		if got := strings.Count(rendered, "internal tmux rebalance-panes"); got != 3 {
			t.Errorf("%s generated config has %d rebalance handler occurrence(s), want exact pane-exited+pane-died+after-kill-pane trio", kind, got)
		}
		for verb := range closedTmuxTopologyMutationVerbs {
			occurrences := regexp.MustCompile(`(^|[[:space:];{}'\"])(`+regexp.QuoteMeta(verb)+`)([[:space:];{}'\"]|$)`).FindAllStringIndex(rendered, -1)
			if len(occurrences) == 0 {
				continue
			}
			if verb != "swap-pane" && verb != "resize-pane" {
				t.Errorf("%s generated config embeds unmanaged lifecycle/topology verb %q", kind, verb)
				continue
			}
			classified := 0
			if verb == "swap-pane" {
				classified = strings.Count(rendered, "swap-pane -U") + strings.Count(rendered, "swap-pane -D")
			} else {
				classified = strings.Count(rendered, "resize-pane -Z")
			}
			if classified != len(occurrences) {
				t.Errorf("%s generated config has %d %s occurrence(s), but only %d match exact closed presentation rows", kind, len(occurrences), verb, classified)
			}
		}
	}
}

func TestGeneratedPaneExitHooksMapExactAllWindowRebalanceEffects(t *testing.T) {
	rows := map[string]runtimeMutationSurface{}
	for _, row := range runtimeMutationSurfaces {
		rows[row.ID] = row
	}
	rebalance := rows["pane.rebalance"]
	if rebalance.Disposition != runtimeMutationSurfaceExempt || rebalance.RootKinds != "all runtime classes" ||
		!strings.Contains(rebalance.OwnerRoute, "every observed multi-Pane Window") ||
		!strings.Contains(rebalance.Handler, "runRebalancePanes") || !strings.Contains(rebalance.Effect, "every observed multi-Pane Window") {
		t.Fatalf("pane.rebalance does not declare its all-window presentation effect: %#v", rebalance)
	}
	for _, test := range []struct {
		reason      controllerTriggerReason
		controller  string
		rebalanceID string
	}{
		{reason: controllerTriggerPaneExited, controller: "trigger.pane-exited", rebalanceID: "trigger.pane-exited.rebalance"},
		{reason: controllerTriggerPaneKilled, controller: "trigger.after-kill-pane", rebalanceID: "trigger.after-kill-pane.rebalance"},
	} {
		controllerRow := rows[test.controller]
		rebalanceRow := rows[test.rebalanceID]
		if controllerRow.Disposition != runtimeMutationSurfaceExempt || !strings.Contains(controllerRow.Guard, "rebalance write is classified separately") {
			t.Fatalf("controller hook half %q hides its generated presentation write: %#v", test.controller, controllerRow)
		}
		if rebalanceRow.Disposition != runtimeMutationSurfaceExempt || rebalanceRow.RootKinds != "all runtime classes" ||
			!strings.Contains(rebalanceRow.OwnerRoute, "every observed multi-Pane Window") ||
			!strings.Contains(rebalanceRow.Handler, "internal tmux rebalance-panes") {
			t.Fatalf("generated hook rebalance row %q is not exact all-window presentation: %#v", test.rebalanceID, rebalanceRow)
		}
		body := tmuxPaneExitHookBody("/usr/local/bin/projmux", test.reason)
		rebalanceAt := strings.Index(body, "internal tmux rebalance-panes")
		controllerAt := strings.Index(body, "internal tmux converge")
		if rebalanceAt < 0 || controllerAt < 0 || rebalanceAt >= controllerAt || strings.Count(body, "internal tmux rebalance-panes") != 1 {
			t.Fatalf("generated %s hook does not execute one rebalance before its controller half: %s", test.reason, body)
		}
	}
}

func TestAIBellIntegrationOwnsRunnerLifecycleTransport(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root := filepath.Dir(thisFile)
	callCounts := map[string]int{}
	err := filepath.WalkDir(root, func(path string, item fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if item.IsDir() || strings.HasSuffix(item.Name(), "_test.go") || !strings.HasSuffix(item.Name(), ".go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if ok && (selector.Sel.Name == "SetHook" || selector.Sel.Name == "SetOption") {
					callCounts[item.Name()+":"+function.Name.Name+":"+selector.Sel.Name]++
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := map[string]int{
		"ai_integrate.go:runTmuxBellCommand:SetHook":   2,
		"ai_integrate.go:runTmuxBellCommand:SetOption": 2,
	}
	if !reflect.DeepEqual(callCounts, wantCalls) {
		t.Fatalf("SetHook/SetOption production callers = %v, want only AI integration %v", callCounts, wantCalls)
	}
	lifecyclePath := filepath.Join(root, "..", "integrations", "mux", "lifecycle.go")
	lifecycle, err := os.ReadFile(lifecyclePath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(lifecycle)
	for _, name := range []string{"SetHook", "SetOption"} {
		if regexp.MustCompile(`(?m)^func ` + name + `\(`).MatchString(text) {
			t.Fatalf("dead package-global %s wrapper reappeared as false product-surface evidence", name)
		}
		if strings.Count(text, "func (r Runner) "+name+"(") != 1 {
			t.Fatalf("Runner.%s transport count changed without audit review", name)
		}
	}
	if strings.Count(text, "return r.Run(ctx, args...)") != 2 {
		t.Fatalf("Runner SetHook/SetOption transports no longer have two exact variable-argv seams")
	}
	rows := map[string]runtimeMutationSurface{}
	for _, row := range runtimeMutationSurfaces {
		rows[row.ID] = row
	}
	if row := rows["ai.integrate-tmux-bell"]; row.Disposition != runtimeMutationSurfaceExempt || !strings.Contains(row.Handler, "bell option and hook") {
		t.Fatalf("AI bell caller/transport lacks its exact semantic surface: %#v", row)
	}
	if row := rows["config.migration"]; strings.Contains(strings.ToLower(row.Producer+" "+row.Handler), "bell") {
		t.Fatalf("config migration falsely claims AI bell lifecycle transport: %#v", row)
	}
}

func TestPropertyPlannedRuntimeMutationIsGuardedOrderedAndIdempotent(t *testing.T) {
	authority := (&runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"}).printable()
	actions := []plannedRuntimeMutation{
		newRuntimeMutation(3, mutationWriteLayout, runtimeMutationTarget{Socket: "-L=property", PhysicalSocket: "/tmp/property", RouteAuthority: authority, Kind: "pane", ID: "%3", UID: "pan-3"}),
		newRuntimeMutation(1, mutationWriteLease, runtimeMutationTarget{Socket: "-L=property", PhysicalSocket: "/tmp/property", RouteAuthority: authority, Kind: "session", ID: "$1", UID: "prj-1"}),
		newRuntimeMutation(2, mutationCreatePane, runtimeMutationTarget{Socket: "-L=property", PhysicalSocket: "/tmp/property", RouteAuthority: authority, Kind: "pane", ID: "%2", UID: "pan-2"}),
	}
	actions[0].Operands = []string{"-t", "%3", "-x", "40"}
	actions[1].Operands = []string{"-t", "$1", createOperationEnvironment, "op-property"}
	actions[2].Operands = []string{"-d", "-t", "%2"}
	left, err := newRuntimeMutationPlan(actions...).printableBytes()
	if err != nil {
		t.Fatal(err)
	}
	right, err := newRuntimeMutationPlan(actions[1], actions[2], actions[0]).printableBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("shuffled input changed plan bytes:\n%s\n---\n%s", left, right)
	}

	writes := 0
	guardFailure := []runtimeMutationStep{
		{Action: actions[2], TargetRouteGuard: func(context.Context) error { return nil }, Reobserve: func(context.Context) (bool, error) { return false, nil }, Guard: func(context.Context) error { return errPropertyDrift }, Apply: func(context.Context) error { writes++; return nil }},
		{Action: actions[0], TargetRouteGuard: func(context.Context) error { return nil }, Reobserve: func(context.Context) (bool, error) { return false, nil }, Guard: func(context.Context) error { return nil }, Apply: func(context.Context) error { writes++; return nil }},
		{Action: actions[1], TargetRouteGuard: func(context.Context) error { return nil }, Reobserve: func(context.Context) (bool, error) { return false, nil }, Guard: func(context.Context) error { return nil }, Apply: func(context.Context) error { writes++; return nil }},
	}
	if err := executeRuntimeMutationPlan(context.Background(), guardFailure); err == nil || !strings.Contains(err.Error(), "before first write") {
		t.Fatalf("guard drift error = %v", err)
	}
	if writes != 0 {
		t.Fatalf("guard drift performed %d writes, want zero", writes)
	}

	var applied, undone []int
	steps := make([]runtimeMutationStep, 0, len(actions))
	for _, action := range actions {
		order := action.Order
		step := runtimeMutationStep{
			Action:           action,
			TargetRouteGuard: func(context.Context) error { return nil },
			Reobserve:        func(context.Context) (bool, error) { return false, nil },
			Guard:            func(context.Context) error { return nil },
			Apply: func(context.Context) error {
				applied = append(applied, order)
				if order == 3 {
					return errPropertyApply
				}
				return nil
			},
		}
		// Order 2 represents a pre-existing effect that this operation does not
		// own. Its absent Undo is the structural rollback authority check.
		if order != 2 {
			step.Undo = func(context.Context) error { undone = append(undone, order); return nil }
		}
		steps = append(steps, step)
	}
	if err := executeRuntimeMutationPlan(context.Background(), steps); err == nil {
		t.Fatal("partial failure unexpectedly succeeded")
	}
	if !reflect.DeepEqual(applied, []int{1, 2, 3}) || !reflect.DeepEqual(undone, []int{3, 1}) {
		t.Fatalf("apply/owned rollback order = %v/%v, want [1 2 3]/[3 1]", applied, undone)
	}

	plan := newRuntimeMutationPlan(actions...)
	observed := map[string]bool{}
	var appliedEffects []string
	success := make([]runtimeMutationStep, 0, len(actions))
	for _, action := range actions {
		success = append(success, runtimeMutationStep{
			Action:           action,
			TargetRouteGuard: func(context.Context) error { return nil },
			Reobserve: func(context.Context) (bool, error) {
				return observed[runtimeMutationEffectKey(action)], nil
			},
			Guard: func(context.Context) error { return nil },
			Apply: func(context.Context) error {
				key := runtimeMutationEffectKey(action)
				appliedEffects = append(appliedEffects, key)
				observed[key] = true
				return nil
			},
		})
	}
	if err := executeRuntimeMutationPlan(context.Background(), success); err != nil {
		t.Fatalf("successful execute: %v", err)
	}
	wantEffects := make([]string, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		wantEffects = append(wantEffects, runtimeMutationEffectKey(action))
	}
	if !reflect.DeepEqual(appliedEffects, wantEffects) {
		t.Fatalf("successful applied effects = %v, want exact total order %v", appliedEffects, wantEffects)
	}
	beforeRepeat := len(appliedEffects)
	if err := executeRuntimeMutationPlan(context.Background(), success); err != nil {
		t.Fatalf("repeat execute: %v", err)
	}
	if len(appliedEffects) != beforeRepeat {
		t.Fatalf("repeat execute performed %d new writes, want zero", len(appliedEffects)-beforeRepeat)
	}
	repeat, err := plan.withoutEffects(observed)
	if err != nil {
		t.Fatal(err)
	}
	if len(repeat.Actions) != 0 {
		t.Fatalf("successful reobserve/replan = %#v, want empty", repeat.Actions)
	}
}

func TestMaterializerOptionEffectUsesTmuxCanonicalBooleanWithoutAcceptingUnknown(t *testing.T) {
	for _, option := range []string{tmuxopts.AutomaticRenameWindow, tmuxopts.RemainOnExitPane} {
		for _, test := range []struct {
			name, want, got string
			observed        bool
		}{
			{name: "off renders zero", want: "off", got: "0", observed: true},
			{name: "on renders one", want: "on", got: "1", observed: true},
			{name: "different boolean", want: "off", got: "1", observed: false},
			{name: "blank is unknown", want: "off", got: "", observed: false},
			{name: "arbitrary value is unknown", want: "off", got: "disabled", observed: false},
		} {
			t.Run(test.name, func(t *testing.T) {
				if got := materializeOptionEffectObserved(option, test.want, test.got); got != test.observed {
					t.Fatalf("observed = %t, want %t", got, test.observed)
				}
			})
		}
	}
	if !materializeOptionEffectObserved(tmuxopts.WindowUID, "win-1", "win-1") ||
		materializeOptionEffectObserved(tmuxopts.WindowUID, "win-1", "win-2") {
		t.Fatal("non-boolean option effect lost exact byte comparison")
	}
}

func TestRuntimeMutationArgvBindsPrintableTargetToExecutableOperand(t *testing.T) {
	action := newRuntimeMutation(1, mutationKillPane, runtimeMutationTarget{
		Socket: "-L=property", PhysicalSocket: "/tmp/property", RouteAuthority: "app:pid=4242/session=/window=/pane=", Kind: "pane", ID: "%1", UID: "pan-1", Parent: "$1/@1",
	})
	action.Operands = []string{"-t", "%2"}
	if _, err := newRuntimeMutationPlan(action).printableBytes(); err == nil || !strings.Contains(err.Error(), "does not match printable target") {
		t.Fatalf("printable plan accepted mismatched executable target: %v", err)
	}
	if _, err := runtimeMutationArgv(action); err == nil || !strings.Contains(err.Error(), "does not match printable target") {
		t.Fatalf("mismatched executable target error = %v", err)
	}

	layout := newRuntimeMutation(1, mutationWriteLayout, runtimeMutationTarget{
		Socket: "-L=property", PhysicalSocket: "/tmp/property", RouteAuthority: "app:pid=4242/session=/window=/pane=", Kind: "pane", ID: "%3", UID: "layout:%3", Parent: "@1",
	})
	layout.Operands = []string{"-t", "%3", "-x", "40"}
	argv, err := runtimeMutationArgv(layout)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"resize-pane", "-t", "%3", "-x", "40"}; !reflect.DeepEqual(argv, want) {
		t.Fatalf("typed layout argv = %v, want %v", argv, want)
	}
}

func TestRuntimeMutationArgvRejectsDuplicateTargetRouteAndSeparatorOperands(t *testing.T) {
	base := newRuntimeMutation(1, mutationKillPane, runtimeMutationTarget{
		Socket: "-L=property", PhysicalSocket: "/tmp/property", Kind: "pane", ID: "%7", UID: "pan-7",
	})
	tests := []struct {
		name     string
		operands []string
	}{
		{name: "duplicate exact target", operands: []string{"-t", "%7", "-t", "%7"}},
		{name: "duplicate overriding target", operands: []string{"-t", "%7", "-t", "%8"}},
		{name: "command separator", operands: []string{"-t", "%7", ";", "kill-server"}},
		{name: "escaped command separator", operands: []string{"-t", "%7", "\\;", "kill-server"}},
		{name: "embedded logical route", operands: []string{"-L", "foreign", "-t", "%7"}},
		{name: "embedded physical route", operands: []string{"-S", "/tmp/foreign", "-t", "%7"}},
		{name: "attached overriding target", operands: []string{"-t", "%7", "-t%8"}},
		{name: "attached logical route", operands: []string{"-t", "%7", "-Lforeign"}},
		{name: "attached physical route", operands: []string{"-t", "%7", "-S/tmp/foreign"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action := base
			action.Operands = tt.operands
			if _, err := runtimeMutationArgv(action); err == nil {
				t.Fatalf("runtimeMutationArgv(%v) accepted hidden execution authority", tt.operands)
			}
		})
	}
	child := newRuntimeMutation(1, mutationCreatePane, runtimeMutationTarget{
		Socket: "-L=property", PhysicalSocket: "/tmp/property", Kind: "pane", ID: "%7", UID: "pan-new", Parent: "$1/@2",
	})
	child.Operands = []string{"-d", "-P", "-F", "#{pane_id}", "-t", "%7"}
	for _, command := range [][]string{{";", "kill-server"}, {"/bin/sh", "\\;", "kill-server"}, {"-t", "%8"}, {"-S/tmp/foreign"}} {
		candidate := child
		candidate.Command = command
		if _, err := runtimeMutationArgv(candidate); err == nil {
			t.Fatalf("runtimeMutationArgv child command %q accepted hidden execution authority", command)
		}
	}
}

func TestRuntimeMutationPlanRejectsMalformedLaterActionBeforeFirstWrite(t *testing.T) {
	authority := (&runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"}).printable()
	first := newRuntimeMutation(1, mutationKillPane, runtimeMutationTarget{
		Socket: "-L=property", PhysicalSocket: "/tmp/property", RouteAuthority: authority, Kind: "pane", ID: "%7", UID: "pan-7",
	})
	first.Operands = []string{"-t", "%7"}
	malformed := newRuntimeMutation(2, mutationKillWindow, runtimeMutationTarget{
		Socket: "-L=property", PhysicalSocket: "/tmp/property", RouteAuthority: authority, Kind: "window", ID: "@8", UID: "win-8",
	})
	malformed.Operands = []string{"-t", "@8", "-t", "@9"}
	writes := 0
	step := func(action plannedRuntimeMutation) runtimeMutationStep {
		return runtimeMutationStep{
			Action:           action,
			TargetRouteGuard: func(context.Context) error { return nil },
			Reobserve:        func(context.Context) (bool, error) { return false, nil },
			Guard:            func(context.Context) error { return nil },
			Apply:            func(context.Context) error { writes++; return nil },
		}
	}
	if err := executeRuntimeMutationPlan(context.Background(), []runtimeMutationStep{step(first), step(malformed)}); err == nil {
		t.Fatal("plan accepted a malformed later action")
	}
	if writes != 0 {
		t.Fatalf("invalid plan performed %d write(s), want zero", writes)
	}
}

func TestRuntimeMutationPlanGuardsPrintableRouteBeforeRepeatEmptyObservation(t *testing.T) {
	authority := (&runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"}).printable()
	action := newRuntimeMutation(1, mutationKillPane, runtimeMutationTarget{
		Socket: "-L=property", PhysicalSocket: "/tmp/property", RouteAuthority: authority,
		Kind: "pane", ID: "%7", UID: "pan-7",
	})
	action.Operands = []string{"-t", "%7"}
	observations, writes := 0, 0
	err := executeRuntimeMutationPlan(context.Background(), []runtimeMutationStep{{
		Action: action,
		TargetRouteGuard: func(context.Context) error {
			return errors.New("printed server generation drifted")
		},
		Reobserve: func(context.Context) (bool, error) { observations++; return true, nil },
		Guard:     func(context.Context) error { return nil },
		Apply:     func(context.Context) error { writes++; return nil },
	}})
	if err == nil || !strings.Contains(err.Error(), "before effect observation") {
		t.Fatalf("printable route refusal = %v", err)
	}
	if observations != 0 || writes != 0 {
		t.Fatalf("authority refusal observations/writes = %d/%d, want 0/0", observations, writes)
	}
}

func TestQueuedMutationBindsPrintableRouteContainmentAndConditionalCleanup(t *testing.T) {
	authority := (&runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"}).printable()
	action := newRuntimeMutation(1, mutationQueuePaneKill, runtimeMutationTarget{
		Socket: "-L=property", PhysicalSocket: "/tmp/property.sock", RouteAuthority: authority, Kind: "pane",
		ID: "%7", UID: "deleted:pan-7", Parent: "$1/@2/prj-1",
	})
	action.Queue = &runtimeMutationQueuedKill{
		PhysicalSocket: "/tmp/property.sock", LogicalSocket: "property", RouteAuthority: authority, ExpectedUID: "deleted:pan-7",
		SessionID: "$1", WindowID: "@2",
	}
	action.Queue.Marker = runtimeMutationQueueMarker(action)
	argv, err := runtimeMutationArgv(action)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"set-environment -g", "run-shell -b", "tmux' -S '/tmp/property.sock'", "#{pid}", "4242", tmuxopts.AppGlobal, runtimeMutationSocketNameOption, "if-shell -F", "#{E:" + action.Queue.Marker + "}", action.Queue.ExpectedUID, "set-environment -gu"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("queued typed argv = %q, want conditional exact-route fragment %q", joined, want)
		}
	}
	if !strings.Contains(joined, "##{E:"+action.Queue.Marker+"}") {
		t.Fatalf("queued typed argv = %q, want inner queue condition delayed past outer run-shell expansion", joined)
	}
	if strings.Contains(joined, "; 'tmux' -S '/tmp/property.sock' set-environment -gu") {
		t.Fatalf("queued cleanup is an unconditional foreign-server write: %q", joined)
	}

	for _, mutate := range []func(*plannedRuntimeMutation){
		func(a *plannedRuntimeMutation) { a.Queue.PhysicalSocket = "/tmp/other.sock" },
		func(a *plannedRuntimeMutation) { a.Queue.LogicalSocket = "other" },
		func(a *plannedRuntimeMutation) { a.Queue.ExpectedUID = "deleted:foreign" },
		func(a *plannedRuntimeMutation) { a.Queue.WindowID = "@9" },
		func(a *plannedRuntimeMutation) { a.Target.Parent = "$9/@2/prj-1" },
	} {
		candidate := action
		queue := *action.Queue
		candidate.Queue = &queue
		mutate(&candidate)
		if _, err := runtimeMutationArgv(candidate); err == nil {
			t.Fatalf("queued mutation accepted printable/executable authority mismatch: %#v", candidate)
		}
	}

	runner := &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-S", action.Target.PhysicalSocket, "display-message", "-p", "-F", "#{socket_path}"):         action.Target.PhysicalSocket + "\n",
		recordedTmuxCallKey("tmux", "-S", action.Target.PhysicalSocket, "show-options", "-gqv", tmuxopts.AppGlobal):              "1\n",
		recordedTmuxCallKey("tmux", "-S", action.Target.PhysicalSocket, "show-options", "-gqv", runtimeMutationSocketNameOption): action.Queue.LogicalSocket + "\n",
		recordedTmuxCallKey("tmux", "-S", action.Target.PhysicalSocket, "show-environment", "-g"):                                action.Queue.Marker + "=deleted:newer-operation\n",
	}}
	if err := clearRuntimeMutationQueueMarker(context.Background(), runner, action); err == nil || !strings.Contains(err.Error(), "newer-operation") {
		t.Fatalf("owned queue-marker clear mismatch error = %v", err)
	}
	for _, call := range runner.calls {
		if slices.Contains(call.args, "-gu") {
			t.Fatalf("foreign/newer queue marker was cleared: %#v", runner.calls)
		}
	}
}

func TestQueuedStandaloneMutationPinsPrintableServerPIDAndBlankRouteClass(t *testing.T) {
	authority := (&runtimeMutationRouteAuthority{Class: runtimeMutationRouteStandalone, ServerPID: "4242", SessionID: "$1", WindowID: "@2", PaneID: "%8"}).printable()
	action := newRuntimeMutation(1, mutationQueuePaneKill, runtimeMutationTarget{
		Socket: "-S=/tmp/standalone.sock", PhysicalSocket: "/tmp/standalone.sock", RouteAuthority: authority,
		Kind: "pane", ID: "%8", UID: "deleted:pan-8", Parent: "$1/@2/prj-1",
	})
	action.Queue = &runtimeMutationQueuedKill{
		PhysicalSocket: "/tmp/standalone.sock", RouteAuthority: authority, ExpectedUID: action.Target.UID,
		SessionID: "$1", WindowID: "@2",
	}
	action.Queue.Marker = runtimeMutationQueueMarker(action)
	argv, err := runtimeMutationArgv(action)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"tmux' -S '/tmp/standalone.sock'", "#{pid}", "4242", tmuxopts.AppGlobal, runtimeMutationSocketNameOption} {
		if !strings.Contains(joined, want) {
			t.Fatalf("standalone queued argv = %q, want %q", joined, want)
		}
	}
	if strings.Contains(joined, tmuxopts.AppGlobal+"},1") || strings.Contains(joined, runtimeMutationSocketNameOption+"},"+defaultAppSocket) {
		t.Fatalf("standalone queued argv smuggled app-owned route authority: %q", joined)
	}

	for _, mutate := range []func(*plannedRuntimeMutation){
		func(candidate *plannedRuntimeMutation) { candidate.Queue.RouteAuthority = "" },
		func(candidate *plannedRuntimeMutation) { candidate.Target.Socket = "-L=projmux" },
		func(candidate *plannedRuntimeMutation) {
			candidate.Queue.RouteAuthority = (&runtimeMutationRouteAuthority{Class: runtimeMutationRouteStandalone, ServerPID: "9999", SessionID: "$1", WindowID: "@2", PaneID: "%8"}).printable()
		},
	} {
		candidate := action
		queue := *action.Queue
		candidate.Queue = &queue
		mutate(&candidate)
		if _, err := runtimeMutationArgv(candidate); err == nil {
			t.Fatalf("standalone queued mutation accepted authority mismatch: %#v", candidate)
		}
	}
}

func TestRuntimeMutationArgvBindsCreateSessionDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		verb   runtimeMutationVerb
		target runtimeMutationTarget
		args   []string
	}{
		{name: "project mismatch", verb: mutationCreateSession,
			target: runtimeMutationTarget{Socket: "-L=property", PhysicalSocket: runtimeMutationSocketAbsentBeforeCreate, Kind: "project-declaration", ID: "project-a", UID: "prj-a"},
			args:   []string{"-f", "/tmp/project-a.conf", "-d", "-s", "project-b"}},
		{name: "project duplicate", verb: mutationCreateSession,
			target: runtimeMutationTarget{Socket: "-L=property", PhysicalSocket: runtimeMutationSocketAbsentBeforeCreate, Kind: "project-declaration", ID: "project-a", UID: "prj-a"},
			args:   []string{"-f", "/tmp/project-a.conf", "-d", "-s", "project-a", "-s", "project-a"}},
		{name: "control mismatch", verb: mutationBootstrapControlSession,
			target: runtimeMutationTarget{Socket: "-L=property", PhysicalSocket: runtimeMutationSocketAbsentBeforeCreate, Kind: "control-session-declaration", ID: "declaration:-L=property/session=home"},
			args:   []string{"-L", "property", "-f", "/tmp/config", "-d", "-s", "foreign"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action := newRuntimeMutation(1, tc.verb, tc.target)
			action.Operands = tc.args
			if _, err := runtimeMutationArgv(action); err == nil {
				t.Fatalf("runtimeMutationArgv(%s) accepted mismatched declaration", tc.name)
			}
		})
	}
	action := newRuntimeMutation(1, mutationCreateSession, runtimeMutationTarget{
		Socket: "-L=property", PhysicalSocket: runtimeMutationSocketAbsentBeforeCreate, Kind: "project-declaration", ID: "project-a", UID: "prj-a",
	})
	action.Operands = []string{"-f", "/tmp/project-a.conf", "-d", "-s", "project-a"}
	argv, err := runtimeMutationArgv(action)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"-f", "/tmp/project-a.conf", "new-session", "-d", "-s", "project-a"}; !reflect.DeepEqual(argv, want) {
		t.Fatalf("typed create argv = %v, want %v", argv, want)
	}

	for _, malformed := range [][]string{
		{"-d", "-s", "project-a", "-f"},
		{"-f", "/tmp/project-a.conf", "-f", "/tmp/other.conf", "-d", "-s", "project-a"},
		{"-d", "-f", "/tmp/project-a.conf", "-s", "project-a"},
	} {
		action.Operands = malformed
		if _, err := runtimeMutationArgv(action); err == nil {
			t.Fatalf("typed create accepted malformed config operands: %#v", malformed)
		}
	}
}

func TestRuntimeMutationArgvBindsBootstrapAndRouteMarkerToPrintableRoute(t *testing.T) {
	bootstrap := newRuntimeMutation(1, mutationBootstrapControlSession, runtimeMutationTarget{
		Socket: "-L=app", PhysicalSocket: runtimeMutationSocketAbsentBeforeCreate, Kind: "control-session-declaration", ID: "declaration:-L=app/session=home",
	})
	bootstrap.Operands = []string{"-L", "other", "-f", "/tmp/config", "-d", "-s", "home"}
	if _, err := runtimeMutationArgv(bootstrap); err == nil || !strings.Contains(err.Error(), "does not match printable route") {
		t.Fatalf("bootstrap route mismatch error = %v", err)
	}
	bootstrap.Operands = []string{"-L", "app", "-S", "/tmp/extra", "-f", "/tmp/config", "-d", "-s", "home"}
	if _, err := runtimeMutationArgv(bootstrap); err == nil {
		t.Fatal("bootstrap accepted an extra executable route")
	}

	marker := newRuntimeMutation(1, mutationWriteRouteMarker, runtimeMutationTarget{
		Socket: "-L=app", PhysicalSocket: "/tmp/app", Kind: "app-server", ID: "socket:/tmp/app", UID: "logical:app",
	})
	marker.Operands = []string{"-L", "app", "-gq", runtimeMutationSocketNameOption, "other"}
	if _, err := runtimeMutationArgv(marker); err == nil || !strings.Contains(err.Error(), "logical identity") {
		t.Fatalf("route-marker logical mismatch error = %v", err)
	}
	marker.Operands = []string{"-gq", "-L", "app", runtimeMutationSocketNameOption, "app"}
	if _, err := runtimeMutationArgv(marker); err == nil || !strings.Contains(err.Error(), "leading exact operand pair") {
		t.Fatalf("misplaced route-marker pair error = %v", err)
	}
	marker.Operands = []string{"-L", "app", "-gq", runtimeMutationSocketNameOption, "other"}
	marker.Operands[len(marker.Operands)-1] = "app"
	argv, err := runtimeMutationArgv(marker)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"-L", "app", "set-option", "-gq", runtimeMutationSocketNameOption, "app"}; !reflect.DeepEqual(argv, want) {
		t.Fatalf("typed route-marker argv = %v, want %v", argv, want)
	}
}

func TestPrintedPhysicalSocketIsExecutionAuthority(t *testing.T) {
	action := newRuntimeMutation(1, mutationKillPane, runtimeMutationTarget{
		Socket: "-L=logical", PhysicalSocket: "/tmp/printed.sock", Kind: "pane", ID: "%7", UID: "pan-7", Parent: "$1/@2",
	})
	action.Operands = []string{"-t", "%7"}
	runner := &recordingTmuxRunner{outputs: map[string]string{}}
	logical, err := tmuxSocketNameTarget("logical")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runRuntimeMutationCommand(context.Background(), explicitTmuxRunner{runner: runner, target: logical}, action); err != nil {
		t.Fatal(err)
	}
	want := recordedTmuxCall{name: "tmux", args: []string{"-S", "/tmp/printed.sock", "kill-pane", "-t", "%7"}}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("execution route = %#v, want printable physical authority %#v", runner.calls, want)
	}

	route := runtimeMutationRoute{target: logical, expectedSocketPath: "/tmp/bound.sock", socketName: "logical"}
	runner.calls = nil
	if err := guardPrintedRuntimeMutationRoute(context.Background(), runner, route, action); err == nil || !strings.Contains(err.Error(), "disagrees") {
		t.Fatalf("print/execution mismatch guard = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("print/execution mismatch reached tmux: %#v", runner.calls)
	}
}

func TestPreMarkerRouteGuardAllowsOnlyTheMissingLogicalEffect(t *testing.T) {
	t.Parallel()

	logical, err := tmuxSocketNameTarget("app")
	if err != nil {
		t.Fatal(err)
	}
	route := runtimeMutationRoute{
		target:             logical,
		expectedSocketPath: "/tmp/tmux-1000/app",
		socketName:         "app",
		authority: &runtimeMutationRouteAuthority{
			Class: runtimeMutationRouteApp, ServerPID: "4242", SessionID: "$1", WindowID: "@1", PaneID: "%1",
		},
	}
	action := newRuntimeMutation(1, mutationWriteRouteMarker, runtimeMutationTarget{
		Socket: "-S=/tmp/tmux-1000/app", PhysicalSocket: "/tmp/tmux-1000/app", RouteAuthority: route.authority.printable(),
		Kind: "session", ID: "$1", UID: "logical:app", Parent: "@1/%1",
	})
	action.Operands = []string{"-S", "/tmp/tmux-1000/app", "-gq", runtimeMutationSocketNameOption, "app"}

	baseOutputs := map[string]string{
		recordedTmuxCallKey("tmux", "-S", route.expectedSocketPath, "display-message", "-p", "-F", "#{socket_path}"): route.expectedSocketPath + "\n",
		recordedTmuxCallKey("tmux", "-S", route.expectedSocketPath, "display-message", "-p", "-F", "#{pid}"):         "4242\n",
		recordedTmuxCallKey("tmux", "-S", route.expectedSocketPath, "show-options", "-gqv", tmuxopts.AppGlobal):      "1\n",
	}
	for _, tc := range []struct {
		name, marker, wantErr string
	}{
		{name: "missing before write"},
		{name: "idempotent exact marker", marker: "app\n"},
		{name: "foreign marker", marker: "foreign\n", wantErr: "logical route marker drifted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outputs := maps.Clone(baseOutputs)
			outputs[recordedTmuxCallKey("tmux", "-S", route.expectedSocketPath, "show-options", "-gqv", runtimeMutationSocketNameOption)] = tc.marker
			runner := &recordingTmuxRunner{outputs: outputs}
			err := guardPrintedRuntimeMutationRouteBeforeMarkerWrite(context.Background(), runner, route, action)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("pre-marker guard error = %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("pre-marker guard error = %v, want %q", err, tc.wantErr)
			}
		})
	}

	outputs := maps.Clone(baseOutputs)
	outputs[recordedTmuxCallKey("tmux", "-S", route.expectedSocketPath, "show-options", "-gqv", runtimeMutationSocketNameOption)] = ""
	if err := guardPrintedRuntimeMutationRoute(context.Background(), &recordingTmuxRunner{outputs: outputs}, route, action); err == nil || !strings.Contains(err.Error(), "logical route marker drifted") {
		t.Fatalf("normal route guard accepted missing post-write marker: %v", err)
	}
}

func TestQueuedMutationEffectReobservesAbsenceAfterMarkerClearRace(t *testing.T) {
	t.Parallel()

	absenceCalls := 0
	markerCalls := 0
	observed, err := observeQueuedRuntimeMutationEffect(context.Background(),
		func(context.Context) (bool, error) {
			absenceCalls++
			return absenceCalls == 2, nil
		},
		func(context.Context) (bool, error) {
			markerCalls++
			return false, nil
		})
	if err != nil || !observed {
		t.Fatalf("completed background kill effect = %v, %v; want observed", observed, err)
	}
	if absenceCalls != 2 || markerCalls != 1 {
		t.Fatalf("observations absence=%d marker=%d, want 2/1", absenceCalls, markerCalls)
	}
}

func TestMaterializerSocketDriftRefusesBeforeFirstWrite(t *testing.T) {
	production := newCreateCommand().runtime
	if _, route, err := production.exactMutationRoute(); err != nil || route != "-L="+defaultAppSocket {
		t.Fatalf("production materializer route = %q, %v; want -L=%s", route, err, defaultAppSocket)
	}
	if got := production.boundMutationTarget("session", "session-name", "uid:project").Socket; got != "-L="+defaultAppSocket {
		t.Fatalf("production printable target route = %q, want -L=%s", got, defaultAppSocket)
	}

	server := newFakeTmux()
	sessions := &fakeSessionMaterializer{tmux: server}
	session := server.addSession("drift")
	runtime := &materializer{
		runner: server, mirror: intmetadata.NewMirror(server), sessions: sessions,
		target:             tmuxTransport{Kind: tmuxSocketPath, Value: server.socketPath, Source: tmuxSocketPathSource},
		expectedSocketPath: server.socketPath,
		socketName:         defaultAppSocket,
		routeAuthority:     &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"},
	}
	server.calls = nil
	server.socketPath = "/tmp/fake-tmux/foreign"
	err := runtime.markCreateOperation(context.Background(), session.id, newRuntimeLedger("op-drift"))
	if err == nil || !strings.Contains(err.Error(), "socket drifted") {
		t.Fatalf("socket drift guard error = %v", err)
	}
	for _, call := range server.calls {
		if slices.Contains(call, "set-environment") {
			t.Fatalf("socket drift reached runtime write: %#v", server.calls)
		}
	}
}

func TestMaterializerAbsentDeclarationRefusesServerAppearingBeforeWrite(t *testing.T) {
	server := newFakeTmux()
	server.addSession("appeared")
	runtime := &materializer{
		runner: server,
		target: tmuxTransport{Kind: tmuxSocketName, Value: defaultAppSocket, Source: tmuxSocketNameSource},
	}
	action := materializeMutationAction(mutationCreateSession, runtime.boundMutationTarget(
		"project-declaration", "appeared", "prj-appeared",
	), "unique Project declaration", "exact atomic create result",
		"-f", "/tmp/projmux-generated.conf", "-d", "-s", "appeared")
	writes := 0
	err := executeRuntimeMutationPlan(context.Background(), []runtimeMutationStep{{
		Action:           action,
		TargetRouteGuard: runtime.targetRouteGuard(action),
		Reobserve:        func(context.Context) (bool, error) { return false, nil },
		Guard:            func(context.Context) error { return nil },
		Apply:            func(context.Context) error { writes++; return nil },
	}})
	if err == nil || !strings.Contains(err.Error(), "absent-before-create server appeared after planning") {
		t.Fatalf("appeared-server plan error = %v", err)
	}
	if writes != 0 {
		t.Fatalf("appeared-server plan writes = %d, want 0", writes)
	}
}

func TestMaterializerUnsetIdentityRequiresReadableOwnedTargetBeforeReplan(t *testing.T) {
	newFixture := func(t *testing.T, ownerUID string) (*materializer, *fakeTmux, string) {
		t.Helper()
		server := newFakeTmux()
		session := server.addSession("identity-unset")
		session.opts[tmuxopts.ProjectUIDSession] = "prj-identity"
		session.windows[0].opts[tmuxopts.WindowUID] = "win-identity"
		pane := session.windows[0].panes[0]
		pane.opts[tmuxopts.PaneUID] = ownerUID
		target := tmuxTransport{Kind: tmuxSocketPath, Value: server.socketPath, Source: tmuxSocketPathSource}
		return &materializer{
			runner: explicitTmuxRunner{runner: server, target: target},
			target: target, expectedSocketPath: server.socketPath,
			socketName:     defaultAppSocket,
			routeAuthority: &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"},
		}, server, pane.id
	}
	assertZeroWrites := func(t *testing.T, server *fakeTmux) {
		t.Helper()
		for _, call := range server.calls {
			if slices.Contains(call, "set-option") || slices.Contains(call, "rename-window") {
				t.Fatalf("unset identity refusal reached a write: %#v", server.calls)
			}
		}
	}

	t.Run("unreadable option is unknown", func(t *testing.T) {
		const uid = "pan-identity"
		runtime, server, paneID := newFixture(t, uid)
		server.fail = []string{"display-message", "-t", paneID, "#{" + aiPaneTopicOption + "}"}
		server.failMessage = "identity option observation unavailable"
		err := runtime.runIdentityWrites(context.Background(), "pane", paneID, uid, []identityPlanWrite{{
			operands: []string{"-p", "-u", "-t", paneID, aiPaneTopicOption},
			effect:   "legacy AI topic projection absent",
		}})
		if err == nil || !strings.Contains(err.Error(), "observation unknown before first write") ||
			!strings.Contains(err.Error(), "identity option observation unavailable") {
			t.Fatalf("unreadable unset observation = %v", err)
		}
		assertZeroWrites(t, server)
	})

	t.Run("absent option on recycled foreign uid refuses", func(t *testing.T) {
		runtime, server, paneID := newFixture(t, "pan-recycled-foreign")
		err := runtime.runIdentityWrites(context.Background(), "pane", paneID, "pan-identity", []identityPlanWrite{{
			operands: []string{"-p", "-u", "-t", paneID, aiPaneTopicOption},
			effect:   "legacy AI topic projection absent",
		}})
		if err == nil || !strings.Contains(err.Error(), "foreign uid \"pan-recycled-foreign\", want \"pan-identity\"") {
			t.Fatalf("recycled foreign unset observation = %v", err)
		}
		assertZeroWrites(t, server)
	})
}

func TestMaterializerReceiptBearingMatchingEffectsStillRequireExactInvariant(t *testing.T) {
	newFixture := func() (*materializer, *fakeTmux, *fakeSessionMaterializer, intmux.NewSessionResult) {
		server := newFakeTmux()
		session := server.addSession("receipt")
		result := intmux.NewSessionResult{
			Created: true, SessionID: session.id,
			WindowID: session.windows[0].id, PaneID: session.windows[0].panes[0].id,
		}
		target := tmuxTransport{Kind: tmuxSocketPath, Value: server.socketPath, Source: tmuxSocketPathSource}
		sessions := &fakeSessionMaterializer{tmux: server}
		return &materializer{
			runner: explicitTmuxRunner{runner: server, target: target}, sessions: sessions,
			target: target, expectedSocketPath: server.socketPath,
			socketName:     defaultAppSocket,
			routeAuthority: &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"},
		}, server, sessions, result
	}
	assertNoWrite := func(t *testing.T, server *fakeTmux) {
		t.Helper()
		for _, call := range server.calls {
			if slices.Contains(call, "set-option") || slices.Contains(call, "set-environment") || slices.Contains(call, "rename-window") {
				t.Fatalf("receipt refusal reached a write: %#v", server.calls)
			}
		}
	}

	t.Run("matching Project path cannot skip mismatched create receipt", func(t *testing.T) {
		runtime, server, _, result := newFixture()
		session := server.session(result.SessionID)
		session.opts[inttmux.ProjectPathSessionOption] = "/work/receipt"
		session.env[createOperationEnvironment] = "newer-operation"
		server.calls = nil
		project := coremetadata.Project{
			Metadata: coremetadata.ObjectMeta{UID: "prj-receipt", Name: "receipt"},
			Spec:     coremetadata.ProjectSpec{Root: "/work/receipt"},
		}
		err := runtime.writeCreatedProjectAnchor(context.Background(), result, project, "op-receipt")
		if err == nil || !strings.Contains(err.Error(), "created Project tuple or operation lease drifted") {
			t.Fatalf("matching path with wrong receipt = %v", err)
		}
		assertNoWrite(t, server)
	})

	t.Run("matching finalize marker cannot skip mismatched create receipt", func(t *testing.T) {
		runtime, server, sessions, result := newFixture()
		session := server.session(result.SessionID)
		session.env[createOperationEnvironment] = "newer-operation"
		session.env[finalizeOperationEnvironment] = "op-receipt"
		startupCalls := 0
		sessions.startup = func() { startupCalls++ }
		server.calls = nil
		err := runtime.finalizeSessionStartup(context.Background(), result, "receipt", "/work/receipt", newRuntimeLedger("op-receipt"))
		if err == nil || !strings.Contains(err.Error(), "create-operation lease changed") {
			t.Fatalf("matching finalize marker with wrong receipt = %v", err)
		}
		if startupCalls != 0 {
			t.Fatalf("mismatched finalize receipt ran startup %d times", startupCalls)
		}
		assertNoWrite(t, server)
	})

	t.Run("matching create marker without atomic result cannot replan empty", func(t *testing.T) {
		runtime, server, _, result := newFixture()
		server.session(result.SessionID).env[createOperationEnvironment] = "op-receipt"
		action := materializeMutationAction(mutationCreateSession,
			runtime.boundMutationTarget("project-declaration", "receipt", "prj-receipt"),
			"unique Project declaration", "exact atomic create result",
			"-d", "-P", "-F", tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}"),
			"-s", "receipt", "-e", createOperationEnvironment+"=op-receipt")
		guardCalls, applyCalls := 0, 0
		err := runtime.runMaterializeMutation(context.Background(), action, func() error {
			guardCalls++
			return errors.New("existing declaration refuses create")
		}, func() error {
			applyCalls++
			return nil
		}, func(context.Context) (bool, error) { return false, nil })
		if err == nil || !strings.Contains(err.Error(), "existing declaration refuses create") {
			t.Fatalf("matching marker without receipt = %v", err)
		}
		if guardCalls != 1 || applyCalls != 0 {
			t.Fatalf("create without receipt guard/apply = %d/%d, want 1/0", guardCalls, applyCalls)
		}
		assertNoWrite(t, server)
	})
}

func TestMaterializerFirstSessionBindsPhysicalRouteBeforeFollowUpWrites(t *testing.T) {
	server := newFakeTmux()
	target := tmuxTransport{Kind: tmuxSocketName, Value: defaultAppSocket, Source: tmuxSocketNameSource}
	routed := explicitTmuxRunner{runner: server, target: target}
	sessions := &fakeSessionMaterializer{tmux: server}
	runtime := &materializer{
		runner: routed, mirror: intmetadata.NewMirror(routed), sessions: sessions, target: target,
	}
	project := coremetadata.Project{
		Metadata: coremetadata.ObjectMeta{UID: "prj-first", Name: "first"},
		Spec:     coremetadata.ProjectSpec{Root: "/work/first"},
	}
	result, err := runtime.ensureSessionAt(context.Background(), project, "first", project.Spec.Root, newRuntimeLedger("op-first"))
	if err != nil {
		t.Fatalf("first-session materialize: %v", err)
	}
	if exactTmuxHandle(result.SessionID, "$") == "" || runtime.expectedSocketPath != server.socketPath {
		t.Fatalf("created result/physical binding = %#v / %q", result, runtime.expectedSocketPath)
	}
	created := false
	for _, call := range server.calls {
		if slices.Contains(call, "new-session") {
			created = true
			continue
		}
		if !created || (!slices.Contains(call, "set-option") && !slices.Contains(call, "set-environment")) {
			continue
		}
		if len(call) < 2 || call[0] != "-S" || call[1] != server.socketPath {
			t.Fatalf("post-create write escaped printed physical route: %#v", call)
		}
	}
}

func TestMaterializerAbsentServerConfigAndRouteMarkerSequence(t *testing.T) {
	newFixture := func(t *testing.T, appMarker, logicalMarker string) (*materializer, *fakeTmux, coremetadata.Project, string) {
		t.Helper()
		configPath := filepath.Join(t.TempDir(), "projmux", "tmux.conf")
		if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, []byte("set-option -g @projmux_app 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		server := newFakeTmux()
		server.serverAbsent = true
		server.appMarker = appMarker
		server.socketName = logicalMarker
		target := tmuxTransport{Kind: tmuxSocketName, Value: "phase10-fresh", Source: tmuxSocketNameSource}
		routed := explicitTmuxRunner{runner: server, target: target}
		runtime := &materializer{
			runner: routed, mirror: intmetadata.NewMirror(routed), sessions: &fakeSessionMaterializer{tmux: server},
			target: target, configPath: configPath,
		}
		project := coremetadata.Project{
			Metadata: coremetadata.ObjectMeta{UID: "prj-fresh", Name: "fresh"},
			Spec:     coremetadata.ProjectSpec{Root: "/work/fresh"},
		}
		return runtime, server, project, configPath
	}

	t.Run("config precedes create and marker precedes managed identity", func(t *testing.T) {
		runtime, server, project, configPath := newFixture(t, "1", "")
		ledger := newRuntimeLedger("op-fresh")
		result, err := runtime.ensureSessionAt(context.Background(), project, "fresh", project.Spec.Root, ledger)
		if err != nil {
			t.Fatalf("fresh Project materialize: %v", err)
		}
		if server.socketName != "phase10-fresh" || runtime.expectedSocketPath != server.socketPath {
			t.Fatalf("fresh route binding = marker %q physical %q", server.socketName, runtime.expectedSocketPath)
		}
		createIndex, markerIndex, identityIndex := -1, -1, -1
		for index, call := range server.calls {
			argv := tmuxCommandArgv(call)
			if slices.Contains(argv, "new-session") {
				createIndex = index
				if !reflect.DeepEqual(argv[:3], []string{"-f", configPath, "new-session"}) {
					t.Fatalf("fresh create global argv = %#v, want -f config before new-session", argv)
				}
			}
			if slices.Contains(argv, runtimeMutationSocketNameOption) && slices.Contains(argv, "set-option") {
				markerIndex = index
				if len(call) < 2 || call[0] != "-S" || call[1] != server.socketPath {
					t.Fatalf("route marker escaped exact physical socket: %#v", call)
				}
			}
			if slices.Contains(argv, tmuxopts.ProjectUIDSession) && slices.Contains(argv, "set-option") {
				identityIndex = index
			}
			if slices.Contains(argv, "show-options") && slices.Contains(argv, tmuxopts.AppGlobal) &&
				(len(call) < 2 || call[0] != "-S" || call[1] != server.socketPath) {
				t.Fatalf("post-bind app ownership read escaped exact physical socket: %#v", call)
			}
		}
		if createIndex < 0 || markerIndex <= createIndex || identityIndex <= markerIndex {
			t.Fatalf("fresh mutation order create=%d marker=%d identity=%d calls=%#v", createIndex, markerIndex, identityIndex, server.calls)
		}

		server.calls = nil
		if err := runtime.writeCreatedProjectRouteMarker(context.Background(), result, ledger.operationMarker); err != nil {
			t.Fatalf("repeat route marker: %v", err)
		}
		for _, call := range server.calls {
			if slices.Contains(call, "set-option") {
				t.Fatalf("repeat route marker was not empty: %#v", server.calls)
			}
		}
	})

	t.Run("create error after effect rebinds logical route and rolls back exact lease", func(t *testing.T) {
		runtime, server, project, _ := newFixture(t, "", "")
		server.fail = []string{"new-session"}
		server.failAfterMutation = true
		server.failMessage = "synchronous hook failed after create"
		_, err := runtime.ensureSessionAt(context.Background(), project, "fresh", project.Spec.Root, newRuntimeLedger("op-after-effect"))
		if err == nil || !strings.Contains(err.Error(), "synchronous hook failed after create") {
			t.Fatalf("fresh create after-effect error = %v", err)
		}
		if runtime.expectedSocketPath != server.socketPath {
			t.Fatalf("recovery physical route = %q, want %q", runtime.expectedSocketPath, server.socketPath)
		}
		if server.session("fresh") != nil {
			t.Fatal("after-effect create failure leaked its uniquely leased session")
		}
		foundLogicalProbe, foundExactKill := false, false
		for _, call := range server.calls {
			argv := tmuxCommandArgv(call)
			if len(call) >= 2 && call[0] == "-L" && call[1] == "phase10-fresh" && slices.Contains(argv, "display-message") {
				foundLogicalProbe = true
			}
			if len(call) >= 2 && call[0] == "-S" && call[1] == server.socketPath && slices.Contains(argv, "kill-session") {
				foundExactKill = true
			}
		}
		if !foundLogicalProbe || !foundExactKill {
			t.Fatalf("after-effect recovery did not rebind -L then kill exact -S: %#v", server.calls)
		}
	})

	t.Run("partial receipt uses one unique exact lease tuple", func(t *testing.T) {
		runtime, server, _, _ := newFixture(t, "", "")
		server.serverAbsent = false
		created := server.addSession("partial")
		marker := "op-partial-receipt"
		created.env[createOperationEnvironment] = marker
		if err := runtime.recoverCreatedProjectByLease(context.Background(), intmux.NewSessionResult{Created: true, SessionID: created.id}, marker); err != nil {
			t.Fatalf("recover partial result from unique lease: %v", err)
		}
		if server.session("partial") != nil {
			t.Fatal("partial result prevented unique lease-owned rollback")
		}
	})

	t.Run("no server is already absent and performs no write", func(t *testing.T) {
		runtime, server, _, _ := newFixture(t, "", "")
		if err := runtime.recoverCreatedProjectByLease(context.Background(), intmux.NewSessionResult{}, "op-no-server"); err != nil {
			t.Fatalf("recover absent server: %v", err)
		}
		for _, call := range server.calls {
			argv := tmuxCommandArgv(call)
			if slices.Contains(argv, "kill-session") || slices.Contains(argv, "set-option") || slices.Contains(argv, "set-environment") {
				t.Fatalf("no-server recovery reached a write: %#v", call)
			}
		}
	})

	t.Run("full receipt mismatch refuses without a kill", func(t *testing.T) {
		runtime, server, _, _ := newFixture(t, "", "")
		server.serverAbsent = false
		created := server.addSession("mismatch")
		marker := "op-full-mismatch"
		created.env[createOperationEnvironment] = marker
		err := runtime.recoverCreatedProjectByLease(context.Background(), intmux.NewSessionResult{
			Created: true, SessionID: "$900", WindowID: "@901", PaneID: "%902",
		}, marker)
		if err == nil || !strings.Contains(err.Error(), "disagrees with unique lease tuple") {
			t.Fatalf("full receipt mismatch error = %v", err)
		}
		if server.session("mismatch") == nil {
			t.Fatal("full receipt mismatch killed the unique lease tuple")
		}
		for _, call := range server.calls {
			if slices.Contains(tmuxCommandArgv(call), "kill-session") {
				t.Fatalf("full receipt mismatch reached a kill: %#v", call)
			}
		}
	})

	t.Run("ambiguous lease refuses without a kill", func(t *testing.T) {
		runtime, server, _, _ := newFixture(t, "", "")
		server.serverAbsent = false
		marker := "op-ambiguous-receipt"
		first := server.addSession("ambiguous-a")
		second := server.addSession("ambiguous-b")
		first.env[createOperationEnvironment] = marker
		second.env[createOperationEnvironment] = marker
		err := runtime.recoverCreatedProjectByLease(context.Background(), intmux.NewSessionResult{}, marker)
		if err == nil || !strings.Contains(err.Error(), "matched 2 exact containments") {
			t.Fatalf("ambiguous lease recovery error = %v", err)
		}
		if server.session("ambiguous-a") == nil || server.session("ambiguous-b") == nil {
			t.Fatal("ambiguous lease recovery killed a session")
		}
		for _, call := range server.calls {
			if slices.Contains(tmuxCommandArgv(call), "kill-session") {
				t.Fatalf("ambiguous lease recovery reached a kill: %#v", call)
			}
		}
	})

	t.Run("existing app server omits startup config and route-marker write", func(t *testing.T) {
		runtime, server, project, configPath := newFixture(t, "1", "phase10-fresh")
		server.serverAbsent = false
		server.addSession("existing")
		if err := os.Remove(configPath); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.ensureSessionAt(context.Background(), project, "fresh", project.Spec.Root, newRuntimeLedger("op-existing")); err != nil {
			t.Fatalf("materialize on existing app server: %v", err)
		}
		foundCreate := false
		for _, call := range server.calls {
			argv := tmuxCommandArgv(call)
			if slices.Contains(argv, "new-session") {
				foundCreate = true
				if slices.Contains(argv, "-f") {
					t.Fatalf("existing-server create carried a startup config: %#v", call)
				}
			}
			if slices.Contains(argv, "set-option") && slices.Contains(argv, runtimeMutationSocketNameOption) {
				t.Fatalf("existing-server create rewrote the logical route marker: %#v", call)
			}
		}
		if !foundCreate {
			t.Fatalf("existing-server materialization issued no create: %#v", server.calls)
		}
	})

	for _, test := range []struct {
		name, appMarker, logicalMarker, want string
	}{
		{name: "generated config lacks app ownership", appMarker: "", want: "not app-owned"},
		{name: "generated config carries forged logical marker", appMarker: "1", logicalMarker: "foreign", want: "logical route marker"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, server, project, _ := newFixture(t, test.appMarker, test.logicalMarker)
			_, err := runtime.ensureSessionAt(context.Background(), project, "fresh", project.Spec.Root, newRuntimeLedger("op-refuse"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("fresh forged config error = %v, want %q", err, test.want)
			}
			if server.session("fresh") != nil {
				t.Fatal("fresh forged config left its lease-owned session behind")
			}
			for _, call := range server.calls {
				argv := tmuxCommandArgv(call)
				if len(argv) > 0 && argv[0] == "kill-session" && (len(call) < 2 || call[0] != "-S" || call[1] != server.socketPath) {
					t.Fatalf("owned rollback escaped exact physical socket: %#v", call)
				}
			}
		})
	}

	t.Run("missing generated config refuses before create", func(t *testing.T) {
		runtime, server, project, configPath := newFixture(t, "1", "")
		if err := os.Remove(configPath); err != nil {
			t.Fatal(err)
		}
		_, err := runtime.ensureSessionAt(context.Background(), project, "fresh", project.Spec.Root, newRuntimeLedger("op-missing-config"))
		if err == nil || !strings.Contains(err.Error(), "is unavailable") {
			t.Fatalf("missing generated config error = %v", err)
		}
		for _, call := range server.calls {
			if slices.Contains(call, "new-session") || slices.Contains(call, "set-option") || slices.Contains(call, "kill-session") {
				t.Fatalf("missing config reached a write: %#v", server.calls)
			}
		}
	})
}

func TestMaterializerDefaultRouteForgedServerRefusesBeforeFirstWrite(t *testing.T) {
	path := "/tmp/projmux-route/cloned-default.sock"
	base := &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "display-message", "-p", "-F", "#{socket_path}"): path + "\n",
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", tmuxopts.AppGlobal):      "0\n",
		recordedTmuxCallKey("tmux", "-S", path, "show-options", "-gqv", tmuxopts.AppGlobal):                  "0\n",
	}}
	target := tmuxTransport{Kind: tmuxSocketName, Value: defaultAppSocket, Source: tmuxSocketNameSource}
	runtime := &materializer{
		runner: explicitTmuxRunner{runner: base, target: target}, target: target, expectedSocketPath: path,
	}
	err := runtime.markCreateOperation(context.Background(), "$1", newRuntimeLedger("op-forged-default"))
	if err == nil || !strings.Contains(err.Error(), "not app-owned") {
		t.Fatalf("forged default materializer error = %v", err)
	}
	for _, call := range base.calls {
		if slices.Contains(call.args, "set-environment") || slices.Contains(call.args, "set-option") {
			t.Fatalf("forged default materializer reached a write: %#v", base.calls)
		}
	}
}

func TestInvocationRoutePreservesNonDefaultLogicalSocketWithoutBasenameInference(t *testing.T) {
	path := "/tmp/projmux-route/not-the-logical-name.sock"
	name := "projmux-it-exact"
	runner := &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-S", path, "display-message", "-p", "-F", "#{socket_path}"):         path + "\n",
		recordedTmuxCallKey("tmux", "-S", path, "show-options", "-gqv", tmuxopts.AppGlobal):              "1\n",
		recordedTmuxCallKey("tmux", "-S", path, "show-options", "-gqv", runtimeMutationSocketNameOption): name + "\n",
		recordedTmuxCallKey("tmux", "-L", name, "display-message", "-p", "-F", "#{socket_path}"):         path + "\n",
		recordedTmuxCallKey("tmux", "-S", path, "display-message", "-p", "-t", "%8", "-F", tmuxRowFormat(
			"#{socket_path}", "#{pid}", "#{session_id}", "#{window_id}", "#{pane_id}")): strings.Join([]string{path, "123", "$1", "@2", "%8"}, tmuxRowSepFormat) + "\n",
	}}
	// Direct resource CLI producers call the anchor-aware resolver with no
	// producer override. The resolver must retain the exact inherited pane.
	route, err := resolveInvocationRuntimeMutationRouteWithAnchor(context.Background(), runner, func(key string) string {
		if key == "TMUX" {
			return path + ",123,0"
		}
		if key == "TMUX_PANE" {
			return "%8"
		}
		return ""
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if route.target.Flag() != "-L" || route.target.Value != name || route.expectedSocketPath != path || route.socketName != name ||
		route.authority == nil || route.authority.Class != runtimeMutationRouteApp || route.authority.ServerPID != "123" || route.authority.PaneID != "%8" {
		t.Fatalf("resolved route = %#v, want logical -L %s over exact path %s", route, name, path)
	}
	if filepath.Base(path) == name {
		t.Fatal("fixture accidentally permits basename inference")
	}
}

func TestStandaloneInheritedRouteMaterializesWithPrintablePIDAndPaneAuthority(t *testing.T) {
	server := newFakeTmux()
	server.appMarker = ""
	server.socketName = ""
	driver := server.addSession("driver")
	driverWindow := driver.windows[0]
	driverPane := driverWindow.panes[0]
	lookup := func(key string) string {
		switch key {
		case "TMUX":
			return server.socketPath + "," + server.serverPID + ",0"
		case "TMUX_PANE":
			return driverPane.id
		default:
			return ""
		}
	}
	route, err := resolveInvocationRuntimeMutationRoute(context.Background(), server, lookup)
	if err != nil {
		t.Fatalf("resolve inherited standalone route: %v", err)
	}
	if route.target.Flag() != "-S" || route.target.Value != server.socketPath || route.expectedSocketPath != server.socketPath ||
		route.socketName != defaultAppSocket || route.authority == nil {
		t.Fatalf("standalone route = %#v", route)
	}
	wantAuthority := "standalone:pid=" + server.serverPID + "/session=" + driver.id + "/window=" + driverWindow.id + "/pane=" + driverPane.id
	if route.authority.printable() != wantAuthority {
		t.Fatalf("standalone printable authority = %q, want %q", route.authority.printable(), wantAuthority)
	}

	routed := explicitTmuxRunner{runner: server, target: route.target}
	runtime := &materializer{
		runner: routed, mirror: intmetadata.NewMirror(routed), sessions: &fakeSessionMaterializer{tmux: server},
		target: route.target, expectedSocketPath: route.expectedSocketPath, routeAuthority: route.authority,
	}
	project := coremetadata.Project{
		Metadata: coremetadata.ObjectMeta{UID: "prj-standalone", Name: "standalone"},
		Spec:     coremetadata.ProjectSpec{Root: t.TempDir()},
	}
	result, err := runtime.ensureSessionAt(context.Background(), project, "standalone-project", project.Spec.Root, newRuntimeLedger("op-standalone"))
	if err != nil {
		t.Fatalf("materialize on inherited standalone route: %v", err)
	}
	if exactTmuxHandle(result.SessionID, "$") == "" || server.session("standalone-project") == nil || server.session("driver") == nil {
		t.Fatalf("standalone materialization result=%#v sessions=%#v", result, server.sessionNames())
	}
	printable := runtime.boundMutationTarget("session", result.SessionID, project.Metadata.UID)
	if printable.Socket != "-S="+server.socketPath || printable.PhysicalSocket != server.socketPath || printable.RouteAuthority != wantAuthority {
		t.Fatalf("standalone printable target = %#v", printable)
	}
	for _, call := range server.calls {
		argv := tmuxCommandArgv(call)
		if (slices.Contains(argv, "new-session") || slices.Contains(argv, "set-option") || slices.Contains(argv, "set-environment")) &&
			(len(call) < 2 || call[0] != "-S" || call[1] != server.socketPath) {
			t.Fatalf("standalone mutation escaped exact physical route: %#v", call)
		}
		if slices.Contains(argv, runtimeMutationSocketNameOption) && slices.Contains(argv, "set-option") {
			t.Fatalf("standalone materialization wrote an app logical marker: %#v", call)
		}
	}
}

func TestStandaloneProducerAnchorSuppliesExactInvocationAuthorityWithoutInheritedPaneEnv(t *testing.T) {
	server := newFakeTmux()
	server.appMarker = ""
	server.socketName = ""
	driver := server.addSession("driver")
	pane := driver.windows[0].panes[0]
	lookup := func(key string) string {
		switch key {
		case "TMUX":
			return server.socketPath + "," + server.serverPID + ",0"
		case runtimeMutationAnchorPaneEnv:
			return pane.id
		default:
			return ""
		}
	}
	route, err := resolveInvocationRuntimeMutationRoute(context.Background(), server, lookup)
	if err != nil {
		t.Fatalf("resolve producer-anchored standalone route: %v", err)
	}
	if route.authority == nil || route.authority.Class != runtimeMutationRouteStandalone ||
		route.authority.PaneID != pane.id || route.authority.WindowID != driver.windows[0].id ||
		route.authority.SessionID != driver.id {
		t.Fatalf("producer-anchored authority = %#v", route.authority)
	}

	if _, err := resolveInvocationRuntimeMutationRouteWithAnchor(context.Background(), server, lookup, "%999"); err == nil {
		t.Fatalf("foreign producer anchor error = %v", err)
	}
}

func TestNativePrivateActivationAnchorSuppliesExactAppAuthorityWithoutInheritedPaneEnv(t *testing.T) {
	server := newFakeTmux()
	driver := server.addSession("driver")
	pane := driver.windows[0].panes[0]
	route, err := resolveInvocationRuntimeMutationRoute(context.Background(), server, func(key string) string {
		switch key {
		case "TMUX":
			return server.socketPath + "," + server.serverPID + ",0"
		case runtimeMutationAnchorPaneEnv:
			return pane.id
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatalf("resolve private-anchored app route: %v", err)
	}
	if route.target != (tmuxTransport{Kind: tmuxSocketName, Value: server.socketName, Source: tmuxSocketNameSource}) ||
		route.expectedSocketPath != server.socketPath || route.authority == nil ||
		route.authority.Class != runtimeMutationRouteApp || route.authority.ServerPID != server.serverPID ||
		route.authority.SessionID != driver.id || route.authority.WindowID != driver.windows[0].id ||
		route.authority.PaneID != pane.id {
		t.Fatalf("private-anchored app authority = %#v", route)
	}
	for _, call := range server.calls {
		if len(call) < 3 || (call[0] != "-L" && call[0] != "-S") {
			t.Fatalf("private-anchored app reobservation used a bare route: %#v", call)
		}
	}
}

func TestRuntimeMutationAnchorSourcesRefuseMalformedHigherPrecedenceEvidence(t *testing.T) {
	for _, test := range []struct {
		name     string
		explicit string
		private  string
		ambient  string
		want     string
		existing bool
	}{
		{name: "invocation explicit cannot fall back", explicit: "pane-seven", want: "explicit anchor"},
		{name: "invocation private cannot fall back", private: "pane-seven", want: "private producer anchor"},
		{name: "invocation inherited malformed", ambient: "pane-seven", want: "inherited TMUX_PANE"},
		{name: "existing explicit cannot fall back", explicit: "pane-seven", want: "explicit anchor", existing: true},
		{name: "existing private cannot fall back", private: "pane-seven", want: "private producer anchor", existing: true},
		{name: "existing inherited malformed", ambient: "pane-seven", want: "inherited TMUX_PANE", existing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newFakeTmux()
			driver := server.addSession("driver")
			ambientPane := driver.windows[0].panes[0].id
			if test.ambient != "" {
				ambientPane = test.ambient
			}
			lookup := func(key string) string {
				switch key {
				case "TMUX":
					return server.socketPath + "," + server.serverPID + ",0"
				case "TMUX_PANE":
					return ambientPane
				case runtimeMutationAnchorPaneEnv:
					return test.private
				default:
					return ""
				}
			}
			var err error
			if test.existing {
				_, err = resolveExistingRuntimeMutationRouteWithAnchor(
					context.Background(), server,
					tmuxTransport{Kind: tmuxSocketPath, Value: server.socketPath, Source: tmuxSocketPathSource}, lookup, test.explicit,
				)
			} else {
				_, err = resolveInvocationRuntimeMutationRouteWithAnchor(context.Background(), server, lookup, test.explicit)
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("malformed higher-precedence anchor error = %v, want %q", err, test.want)
			}
			if len(server.calls) != 0 {
				t.Fatalf("malformed higher-precedence anchor reached tmux before refusal: %#v", server.calls)
			}
		})
	}
}

func TestNativePrivateActivationAnchorNegativeAuthorityMatrixIsFirstWriteZero(t *testing.T) {
	for _, test := range []struct {
		name        string
		privatePane string
		receiptPath string
		receiptPID  string
		appMarker   string
		logical     string
		want        string
	}{
		{name: "stale Pane", privatePane: "%999", want: "reobserve inherited anchor"},
		{name: "malformed Pane", privatePane: "pane-nine", want: "private producer anchor"},
		{name: "wrong server path", receiptPath: "/tmp/projmux-route/foreign.sock", want: "inherited socket drifted"},
		{name: "stale server PID", receiptPID: "999999", want: "containment drifted"},
		{name: "foreign ownership marker", appMarker: "0", want: "not app-owned"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := newFakeTmux()
			driver := server.addSession("driver")
			anchor := driver.windows[0].panes[0].id
			if test.privatePane != "" {
				anchor = test.privatePane
			}
			path := server.socketPath
			if test.receiptPath != "" {
				path = test.receiptPath
			}
			pid := server.serverPID
			if test.receiptPID != "" {
				pid = test.receiptPID
			}
			if test.appMarker != "" {
				server.appMarker = test.appMarker
			}
			if test.logical != "" {
				server.socketName = test.logical
			}
			_, err := resolveInvocationRuntimeMutationRoute(context.Background(), server, func(key string) string {
				switch key {
				case "TMUX":
					return path + "," + pid + ",0"
				case runtimeMutationAnchorPaneEnv:
					return anchor
				case "TMUX_PANE":
					return "%998"
				default:
					return ""
				}
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("negative native authority error = %v, want %q", err, test.want)
			}
			for _, call := range server.calls {
				argv := tmuxCommandArgv(call)
				for _, write := range []string{"set-option", "set-environment", "new-session", "new-window", "split-window", "kill-pane", "kill-window", "kill-session"} {
					if slices.Contains(argv, write) {
						t.Fatalf("negative native authority reached first write %q: %#v", write, server.calls)
					}
				}
			}
		})
	}
}

func TestStandaloneRouteRequiresBlankClassAndExactInheritedPaneReceipt(t *testing.T) {
	for _, test := range []struct {
		name, appMarker, logicalMarker, pane string
		want                                 string
	}{
		{name: "partial app marker", appMarker: "1", want: "projmux config apply --socket projmux"},
		{name: "partial logical marker", logicalMarker: "forged", want: "not app-owned"},
		{name: "missing pane receipt", want: "TMUX_PANE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newFakeTmux()
			server.appMarker, server.socketName = test.appMarker, test.logicalMarker
			driver := server.addSession("driver")
			pane := test.pane
			if test.name != "missing pane receipt" {
				pane = driver.windows[0].panes[0].id
			}
			_, err := resolveInvocationRuntimeMutationRoute(context.Background(), server, func(key string) string {
				if key == "TMUX" {
					return server.socketPath + "," + server.serverPID + ",0"
				}
				if key == "TMUX_PANE" {
					return pane
				}
				return ""
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("standalone classification error = %v, want %q", err, test.want)
			}
			for _, call := range server.calls {
				argv := tmuxCommandArgv(call)
				if slices.Contains(argv, "new-session") || slices.Contains(argv, "set-option") || slices.Contains(argv, "set-environment") || slices.Contains(argv, "kill-session") {
					t.Fatalf("standalone classification refusal reached a write: %#v", call)
				}
			}
		})
	}
}

func TestDefaultInvocationAliasRecoversCanonicalAppRouteBidirectionally(t *testing.T) {
	path, name := "/tmp/projmux-route/canonical.sock", "projmux-it-canonical"
	runner := &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "display-message", "-p", "-F", "#{socket_path}"):         path + "\n",
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", tmuxopts.AppGlobal):              "1\n",
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", runtimeMutationSocketNameOption): name + "\n",
		recordedTmuxCallKey("tmux", "-L", name, "display-message", "-p", "-F", "#{socket_path}"):                     path + "\n",
	}}
	route, err := resolveInvocationRuntimeMutationRoute(context.Background(), runner, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if route.target.Flag() != "-L" || route.target.Value != name || route.socketName != name || route.expectedSocketPath != path ||
		route.authority == nil || route.authority.Class != runtimeMutationRouteApp || route.authority.ServerPID != "4242" || route.authority.PaneID != "" {
		t.Fatalf("canonical alias route = %#v", route)
	}
}

func TestPre013DefaultAppRouteReturnsTypedRecoveryAndZeroWrites(t *testing.T) {
	server := newFakeTmux()
	server.socketName = ""
	server.addSession("legacy")
	_, err := resolveInvocationRuntimeMutationRoute(context.Background(), server, func(string) string { return "" })
	var markerErr *runtimeMutationMarkerError
	if !errors.As(err, &markerErr) || markerErr.Diagnosis != runtimeMutationMarkerMissing || markerErr.LogicalSocket != defaultAppSocket {
		t.Fatalf("missing marker diagnosis = %#v / %v", markerErr, err)
	}
	wantRecovery := "projmux config apply --socket " + defaultAppSocket
	if !strings.Contains(err.Error(), wantRecovery) {
		t.Fatalf("missing marker error = %q, want recovery %q", err, wantRecovery)
	}
	if server.session("legacy") == nil {
		t.Fatal("ordinary diagnosis killed the live legacy session")
	}
	for _, call := range server.calls {
		argv := tmuxCommandArgv(call)
		for _, write := range []string{"set-option", "set-environment", "source-file", "new-session", "new-window", "split-window", "kill-pane", "kill-window", "kill-session"} {
			if slices.Contains(argv, write) {
				t.Fatalf("ordinary missing-marker diagnosis reached write %q: %#v", write, server.calls)
			}
		}
	}
}

func TestDetachedExplicitAnchorBindsExactAppRouteWithoutAmbientEnvironment(t *testing.T) {
	path, pane := "/tmp/projmux-route/sidebar.sock", "%8"
	receipt := strings.Join([]string{path, "4242", "$1", "@2", pane}, tmuxRowSepFormat) + "\n"
	runner := &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "display-message", "-p", "-F", "#{socket_path}"):         path + "\n",
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", tmuxopts.AppGlobal):              "1\n",
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", runtimeMutationSocketNameOption): defaultAppSocket + "\n",
		recordedTmuxCallKey("tmux", "-S", path, "display-message", "-p", "-t", pane, "-F", tmuxRowFormat(
			"#{socket_path}", "#{pid}", "#{session_id}", "#{window_id}", "#{pane_id}")): receipt,
	}}
	route, err := resolveInvocationRuntimeMutationRouteWithAnchor(context.Background(), runner, func(string) string { return "" }, pane)
	if err != nil {
		t.Fatal(err)
	}
	if route.authority == nil || route.authority.Class != runtimeMutationRouteApp || route.authority.ServerPID != "4242" ||
		route.authority.SessionID != "$1" || route.authority.WindowID != "@2" || route.authority.PaneID != pane {
		t.Fatalf("detached explicit route = %#v", route)
	}
}

func TestDetachedExplicitAnchorAuthorityNegativeMatrixIsFirstWriteZero(t *testing.T) {
	for _, test := range []struct {
		name, pane, receiptPath, receiptPID, appMarker, want string
	}{
		{name: "stale Pane", pane: "%999", want: "reobserve explicit anchor"},
		{name: "wrong socket", pane: "%8", receiptPath: "/tmp/projmux-route/foreign.sock", want: "containment drifted"},
		{name: "wrong server PID", pane: "%8", receiptPID: "9999", want: "containment drifted"},
		{name: "foreign ownership", pane: "%8", appMarker: "0", want: "not app-owned"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := "/tmp/projmux-route/sidebar.sock"
			receiptPath, receiptPID := path, "4242"
			if test.receiptPath != "" {
				receiptPath = test.receiptPath
			}
			if test.receiptPID != "" {
				receiptPID = test.receiptPID
			}
			appMarker := "1"
			if test.appMarker != "" {
				appMarker = test.appMarker
			}
			receiptKey := recordedTmuxCallKey("tmux", "-S", path, "display-message", "-p", "-t", test.pane, "-F", tmuxRowFormat(
				"#{socket_path}", "#{pid}", "#{session_id}", "#{window_id}", "#{pane_id}"))
			runner := &recordingTmuxRunner{outputs: map[string]string{
				recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "display-message", "-p", "-F", "#{socket_path}"):         path + "\n",
				recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", tmuxopts.AppGlobal):              appMarker + "\n",
				recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", runtimeMutationSocketNameOption): defaultAppSocket + "\n",
				receiptKey: strings.Join([]string{receiptPath, receiptPID, "$1", "@2", test.pane}, tmuxRowSepFormat) + "\n",
			}}
			if test.name == "stale Pane" {
				runner.errors = map[string]error{receiptKey: errors.New("can't find pane")}
			}
			_, err := resolveInvocationRuntimeMutationRouteWithAnchor(context.Background(), runner, func(string) string { return "" }, test.pane)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("negative explicit anchor error = %v, want %q", err, test.want)
			}
			for _, call := range runner.calls {
				argv := tmuxCommandArgv(call.args)
				for _, write := range []string{"set-option", "set-environment", "new-session", "new-window", "split-window", "kill-pane", "kill-window", "kill-session"} {
					if slices.Contains(argv, write) {
						t.Fatalf("negative explicit anchor reached write %q: %#v", write, runner.calls)
					}
				}
			}
		})
	}
}

func TestSidebarProjectOpenRouteRejectsActualPaneProjectOwnershipMismatchWithoutWrites(t *testing.T) {
	for _, test := range []struct {
		name        string
		windowUID   string
		paneUID     string
		wantErrText string
	}{
		{name: "unmanaged exact client anchor"},
		{name: "matching Project ownership", windowUID: "win-alpha-main", paneUID: "pan-alpha-zsh"},
		{name: "Pane belongs to another Project", windowUID: "win-alpha-main", paneUID: "pan-beta-zsh", wantErrText: "Pane/Window ownership mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			registryBefore := store.snapshot()
			server := newFakeTmux()
			session := server.addSession("alpha")
			window, pane := session.windows[0], session.windows[0].panes[0]
			if test.windowUID != "" {
				window.opts[tmuxopts.WindowUID] = test.windowUID
			}
			if test.paneUID != "" {
				pane.opts[tmuxopts.PaneUID] = test.paneUID
			}

			err := validateSidebarProjectOpenRoute(
				context.Background(), server, func(string) string { return "" }, store.store().snapshot, pane.id,
			)
			if test.wantErrText == "" {
				if err != nil {
					t.Fatalf("matching ownership error = %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.wantErrText) {
				t.Fatalf("ownership mismatch error = %v, want %q", err, test.wantErrText)
			}
			if store.transactions != 0 || store.writes != 0 || store.snapshot() != registryBefore {
				t.Fatalf("ownership validation changed Registry: transactions=%d writes=%d changed=%t",
					store.transactions, store.writes, store.snapshot() != registryBefore)
			}
			if writes := tmuxMutationCallCount(server); writes != 0 {
				t.Fatalf("ownership validation issued %d tmux writes: %#v", writes, server.calls)
			}
		})
	}
}

func TestOutsideTmuxAppRouteSuppressesOnlyTypedNoServerProbe(t *testing.T) {
	probe := recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "display-message", "-p", "-F", "#{socket_path}")
	for _, test := range []struct {
		name    string
		failure inttmux.CommandFailure
		wantOK  bool
		want    string
	}{
		{
			name: "expected absent app server is an internal route outcome",
			failure: inttmux.CommandFailure{Kind: inttmux.CommandFailureExit,
				Stderr: "no server running on /tmp/projmux-route/app.sock"},
			wantOK: true,
		},
		{
			name: "permission stays visible",
			failure: inttmux.CommandFailure{Kind: inttmux.CommandFailurePermission,
				Stderr: "failed to connect to server: Permission denied"},
			want: "Permission denied",
		},
		{
			name: "generic protocol refusal stays visible",
			failure: inttmux.CommandFailure{Kind: inttmux.CommandFailureExit,
				Stderr: "protocol version mismatch"},
			want: "protocol version mismatch",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingTmuxRunner{errors: map[string]error{
				probe: appTypedCommandFailure{failure: test.failure},
			}}
			route, err := resolveInvocationRuntimeMutationRoute(context.Background(), runner, func(string) string { return "" })
			if test.wantOK {
				if err != nil || route.target != (tmuxTransport{Kind: tmuxSocketName, Value: defaultAppSocket, Source: tmuxSocketNameSource}) || route.authority != nil {
					t.Fatalf("typed no-server route = %#v err=%v", route, err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "probe default logical socket") || inttmux.IsNoServerFailure(err) {
					t.Fatalf("visible route error = %v, want non-absence routed failure", err)
				}
				var carrier interface{ CommandFailure() inttmux.CommandFailure }
				if !errors.As(err, &carrier) || carrier.CommandFailure() != test.failure {
					t.Fatalf("route failure lost typed diagnostic: err=%v carrier=%#v", err, carrier)
				}
			}
			if len(runner.calls) != 1 || len(runner.calls[0].args) < 2 || runner.calls[0].args[0] != "-L" || runner.calls[0].args[1] != defaultAppSocket {
				t.Fatalf("outside route probe argv = %#v", runner.calls)
			}
		})
	}
}

func TestInheritedRuntimeMutationReceiptParserIsClosed(t *testing.T) {
	for _, value := range []string{
		"/tmp/socket,1", "/tmp/socket,1,0,extra", "relative,1,0", "/tmp/../tmp/socket,1,0",
		"/tmp/socket,0,0", "/tmp/socket,-1,0", "/tmp/socket,1,-1", "/tmp/socket,1,client",
	} {
		if _, err := parseInheritedTmuxReceipt(value); err == nil {
			t.Errorf("parseInheritedTmuxReceipt(%q) succeeded", value)
		}
	}
	got, err := parseInheritedTmuxReceipt("/tmp/socket,42,0")
	if err != nil || got.SocketPath != "/tmp/socket" || got.ServerPID != "42" || got.ClientID != "0" {
		t.Fatalf("exact inherited receipt = %#v / %v", got, err)
	}
}

func TestForgedLogicalRouteMarkerOnForeignServerRefuses(t *testing.T) {
	path := "/tmp/projmux-route/foreign.sock"
	runner := &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-S", path, "display-message", "-p", "-F", "#{socket_path}"): path + "\n",
		recordedTmuxCallKey("tmux", "-S", path, "show-options", "-gqv", tmuxopts.AppGlobal):      "0\n",
	}}
	_, err := resolveInvocationRuntimeMutationRoute(context.Background(), runner, func(key string) string {
		if key == "TMUX" {
			return path + ",1,0"
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "not app-owned") {
		t.Fatalf("foreign route error = %v", err)
	}
	for _, call := range runner.calls {
		joined := strings.Join(call.args, " ")
		if strings.Contains(joined, "set-option") || strings.Contains(joined, "new-") || strings.Contains(joined, "kill-") {
			t.Fatalf("forged marker refusal wrote tmux: %#v", runner.calls)
		}
	}
}

var (
	errPropertyDrift = &planPropertyError{"guard drift"}
	errPropertyApply = &planPropertyError{"injected partial failure"}
)

type planPropertyError struct{ text string }

func (e *planPropertyError) Error() string { return e.text }

func TestObservationFailureNeverPlansRegistryDeletion(t *testing.T) {
	plan := printableRuntimeMutationInventory()
	repeat, err := plan.withoutEffects(nil)
	if err == nil || !strings.Contains(err.Error(), "observation is unknown") {
		t.Fatalf("unknown observation = %#v, %v", repeat, err)
	}
	if len(repeat.Actions) != 0 {
		t.Fatalf("unknown observation planned actions: %#v", repeat.Actions)
	}
	for verb := range runtimeMutationInventory {
		if strings.Contains(string(verb), "registry") || strings.Contains(string(verb), "delete-row") {
			t.Fatalf("runtime inventory can delete Registry state on observation failure: %q", verb)
		}
	}

	store := newFakeResourceStore(t)
	runtime, runner, _ := newPaneRuntimeFixture(t, paneRuntimeInventory())
	format := tmuxRowFormat("#{session_id}", "#{session_name}", "#{window_id}", "#{pane_id}",
		"#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.WindowUID+"}", "#{"+tmuxopts.PaneUID+"}")
	listKey := recordedTmuxCallKey("tmux", "-S", testDeleteTarget.Value, "list-panes", "-a", "-F", format)
	runner.errors = map[string]error{listKey: errPropertyDrift}
	command := newTestDeleteCommand(store, false, false, nil)
	command.panes = runtime
	before := store.snapshot()
	out, _, routeErr := runRoute(t, command, "pane", "uid:pan-alpha-log", "--yes")
	if routeErr == nil || !strings.Contains(routeErr.Error(), "inventory exact tmux socket") {
		t.Fatalf("production observation failure = %v", routeErr)
	}
	if out != "" || store.snapshot() != before || store.transactions != 0 {
		t.Fatalf("observation failure changed Registry/output: out=%q transactions=%d", out, store.transactions)
	}
	for _, call := range runner.calls {
		joined := strings.Join(call.args, " ")
		for _, forbidden := range []string{"kill-pane", "set-option", "run-shell"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("observation failure reached runtime mutation %q: %#v", forbidden, runner.calls)
			}
		}
	}
}

func TestNativePhase2FilesContainNoRawLifecycleTopologyProducer(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root := filepath.Dir(thisFile)
	for _, name := range []string{"create_agent.go", "agent_resume.go", "supervise_launch.go"} {
		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			if literal, ok := node.(*ast.BasicLit); ok && literal.Kind == token.STRING {
				value, err := strconv.Unquote(literal.Value)
				if err == nil && closedTmuxTopologyMutationVerbs[value] {
					t.Errorf("Phase 2-owned file %s embeds raw lifecycle/topology producer %q", name, value)
				}
			}
			if call, ok := node.(*ast.CallExpr); ok {
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
					switch selector.Sel.Name {
					case "NewSession", "NewWindow", "NewEphemeralSession", "KillSession":
						t.Errorf("Phase 2-owned file %s calls raw lifecycle helper %s", name, selector.Sel.Name)
					}
				}
			}
			return true
		})
	}
}

func TestPlanOnlyMutationNegativeAuditHasZeroBypass(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root := filepath.Dir(thisFile)
	excludedFiles := map[string]string{
		"create_agent.go": "Phase 2 native create owner", "agent_resume.go": "Phase 2 native resume owner",
		"supervise_launch.go": "Phase 2 native launch owner", "runtime_mutation_plan.go": "the typed executor itself",
		"runtime_mutation_surface.go": "the closed inventory declaration",
	}
	var files []string
	for _, scanRoot := range []string{root, filepath.Join(root, "..", "core", "controller"), filepath.Join(root, "..", "integrations", "mux"), filepath.Join(root, "..", "integrations", "metadata"), filepath.Join(root, "..", "integrations", "sessionstate"), filepath.Join(root, "..", "integrations", "tmux")} {
		if err := filepath.WalkDir(scanRoot, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			if _, excluded := excludedFiles[rel]; !excluded {
				files = append(files, rel)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(files)
	mutating := map[string]bool{
		"set-option": true, "set-environment": true, "select-layout": true, "rename-window": true,
		"set-hook": true, "source-file": true, "unbind-key": true,
	}
	for verb := range closedTmuxTopologyMutationVerbs {
		mutating[verb] = true
	}
	mutatingHelpers := map[string]bool{
		"NewEphemeralSession": true, "KillSession": true,
		"MirrorProject": true, "MirrorWindow": true, "MirrorPane": true, "RenameWindow": true,
		"MirrorControlSessionRole": true, "DisableAutomaticRename": true, "RebindProject": true,
		"SetOption": true, "SetHook": true,
	}
	requiredPlanSites := map[string]bool{
		"materialize.go:finalizeSessionStartup":                            true,
		"materialize.go:rollback":                                          true,
		"materialize.go:ensureSessionAt":                                   true,
		"materialize.go:writeCreatedProjectRouteMarker":                    true,
		"materialize.go:recoverCreatedProjectByLease":                      true,
		"materialize.go:claimRuntimeUID":                                   true,
		"materialize.go:mirrorWindow":                                      true,
		"materialize.go:mirrorPane":                                        true,
		"materialize.go:recordErrorCreatedSession":                         true,
		"materialize.go:markCreateOperation":                               true,
		"materialize.go:clearCreateOperations":                             true,
		"materialize.go:newWindow":                                         true,
		"materialize.go:splitPane":                                         true,
		"materialize.go:equalizeSplitLayout":                               true,
		"delete_pane_runtime.go:killAll":                                   true,
		"delete_pane_runtime.go:tombstoneSelfKill":                         true,
		"delete_pane_runtime.go:restoreSelfKill":                           true,
		"delete_pane_runtime.go:queueSelfKill":                             true,
		"delete_window_runtime.go:killAll":                                 true,
		"delete_window_runtime.go:queueSelfKill":                           true,
		"runtime_stop.go:executeManagedRuntimeStop":                        true,
		"shell.go:provisionAppSession":                                     true,
		"shell.go:prepareControlSession":                                   true,
		"shell.go:rollbackControlBootstrap":                                true,
		"create_intent.go:renameWindowFromIntent":                          true,
		"rename.go:renameRuntimeWindow":                                    true,
		"unmanaged_runtime_stop.go:executeUnmanagedRuntimeStop":            true,
		"tmux.go:writeRuntimeMutationRouteMarker":                          true,
		"control_session.go:executeControlSessionIdentityPlan":             true,
		"runtime_metadata_mirror.go:MirrorWindow":                          true,
		"runtime_metadata_mirror.go:MirrorPane":                            true,
		"runtime_metadata_mirror.go:MirrorProject":                         true,
		"controller_runtime_mutation.go:executeControllerRuntimeMutations": true,
		"resource_controller.go:converge":                                  true,
		"controller_trigger.go:runAutomaticMirrorRecovery":                 true,
		"registry_topology_materialize.go:execute":                         true,
	}
	// These are semantic exemptions, keyed to one exact source function and
	// verb. They are intentionally not a broad verb allowlist.
	exemptRawSites := map[string]string{
		"../integrations/sessionstate/replay.go:replay:rename-window":                     "replay.retired-snapshot",
		"../integrations/sessionstate/replay.go:replay:select-layout":                     "replay.retired-snapshot",
		"../integrations/tmux/client.go:CreateEphemeralSession:set-option":                "attach.ephemeral-create",
		"../integrations/tmux/client.go:applyProjectSessionEnv:set-environment":           "attach.ephemeral-create",
		"../integrations/tmux/client.go:setProjectPathAnchor:set-option":                  "attach.ephemeral-create",
		"../integrations/tmux/client.go:markStartupPane:set-option":                       "startup.presentation",
		"../integrations/tmux/client.go:KillSession:kill-session":                         "attach.ephemeral-prune",
		"tmux.go:runAutosaveSessionState:set-option":                                      "sessionstate.autosave-marker",
		"tmux.go:runRebalancePanes:select-layout":                                         "pane.rebalance",
		"tmux.go:runRenamePane:set-option":                                                "catalog.pane.rename",
		"../integrations/tmux/client.go:createDetachedSession:helper:NewEphemeralSession": "attach.ephemeral-create",
		"attach.go:executeAutoAttachPlan:helper:KillSession":                              "attach.ephemeral-prune",
		"prune.go:runEphemeral:helper:KillSession":                                        "standalone.prune",
		"materialize.go:read:variable-argv":                                               "runtime.observation",
		"tmux.go:managedIngestMigrationAIForRoute:variable-argv":                          "config.migration",
		"tmux.go:runApply:source-file":                                                    "config.apply-source",
		"tmux.go:retireGeneratedKeySequenceState:variable-argv":                           "key-sequence.retirement",
		"tmux.go:restoreSidebarOriginSession:variable-argv":                               "sidebar.origin-restore",
		"ai.go:readMux:variable-argv":                                                     "runtime.observation",
		"ai.go:readTrimmed:variable-argv":                                                 "runtime.observation",
		"../integrations/mux/lifecycle.go:Runner.SetHook:variable-argv":                   "ai.integrate-tmux-bell",
		"../integrations/mux/lifecycle.go:Runner.SetOption:variable-argv":                 "ai.integrate-tmux-bell",
		"../integrations/mux/lifecycle.go:Runner.NewEphemeralSession:variable-argv":       "attach.ephemeral-create",
		"../integrations/sessionstate/replay.go:replay:variable-argv":                     "replay.retired-snapshot",
		"../integrations/sessionstate/replay.go:replayPaneIdentityMetadata:variable-argv": "replay.retired-snapshot",
		"../integrations/tmux/client.go:OpenSession:variable-argv":                        "sidebar.origin-restore",
		"../integrations/tmux/client.go:OpenSessionTarget:variable-argv":                  "sidebar.origin-restore",
		"../integrations/tmux/client.go:DisplayPopupWithOptions:variable-argv":            "popup.display",
		"../integrations/metadata/inventory.go:Run:variable-argv":                         "runtime.observation",
		"../integrations/mux/interactive.go:DisplayPopup:variable-argv":                   "popup.display",
		"../integrations/mux/interactive.go:ClosePopup:variable-argv":                     "popup.display",
		"../integrations/mux/interactive.go:SwitchClient:variable-argv":                   "sidebar.origin-restore",
		"../integrations/mux/interactive.go:SelectPane:variable-argv":                     "sidebar.origin-restore",
		"../integrations/mux/interactive.go:SelectWindow:variable-argv":                   "sidebar.origin-restore",
		"../integrations/mux/runner.go:Run:variable-argv":                                 "runtime.observation",
		"../integrations/mux/runner.go:SetPaneOption:variable-argv":                       "agent.presentation",
		"../integrations/mux/runner.go:UnsetPaneOption:variable-argv":                     "agent.presentation",
		"../integrations/mux/runner.go:Read:variable-argv":                                "runtime.observation",
		"../integrations/tmux/sessionstate.go:MarkSessionStateSource:set-option":          "sessionstate.replay-metadata",
		"../integrations/tmux/sessionstate.go:setSessionStateAIResumeMetadata:set-option": "sessionstate.replay-metadata",
		"agent_interaction.go:WriteTopic:set-option":                                      "agent.presentation",
		"agent_interaction.go:WriteInteraction:set-option":                                "agent.presentation",
		"ai_ingest_codex.go:applyCodexHookSemanticDelivery:variable-argv":                 "agent.presentation",
		"ai_ingest_codex_native.go:Apply:variable-argv":                                   "agent.presentation",
		"ai_ingest_codex_native.go:ApplyProgress:variable-argv":                           "agent.presentation",
		"ai_ingest_codex_native.go:SetAuthority:variable-argv":                            "codex.native-lifecycle-authority",
		"ai_ingest_codex_native.go:recordAINotification:set-option":                       "agent.presentation",
		"attention.go:run:variable-argv":                                                  "agent.presentation",
		"binding_convergence.go:Run:variable-argv":                                        "binding.convergence",
		"create_reentrancy.go:deferBindingConvergence:set-environment":                    "create.reentrancy",
		"focus.go:listTargets:variable-argv":                                              "runtime.observation",
		"focus.go:translatePaneIDToTarget:variable-argv":                                  "runtime.observation",
		"focus.go:listSessionInventory:variable-argv":                                     "runtime.observation",
		"focus.go:listClients:variable-argv":                                              "runtime.observation",
		"hook_trust_popup.go:runTmuxHookTrustPopup:variable-argv":                         "popup.display",
		"notify.go:focusNotification:variable-argv":                                       "notification.focus",
		"runtime_diagnostics.go:socketPath:variable-argv":                                 "runtime.observation",
		"status.go:readTrimmed:variable-argv":                                             "runtime.observation",
		"status.go:Run:variable-argv":                                                     "runtime.observation",
		"statusbar.go:handlePopupToggleWithClient:variable-argv":                          "popup.display",
		"statusbar.go:handleNotify:variable-argv":                                         "notification.focus",
		"statusbar.go:runTmuxNoFallback:variable-argv":                                    "runtime.observation",
		"welcome_command.go:runPopup:variable-argv":                                       "popup.display",
		"quit.go:shutdownAppRuntime:kill-server":                                          "app.quit",
		"ai_integrate.go:runTmuxBellCommand:helper:SetOption":                             "ai.integrate-tmux-bell",
		"ai_integrate.go:runTmuxBellCommand:helper:SetHook":                               "ai.integrate-tmux-bell",
		"ai_integrate.go:runTmuxBellCommand:variable-argv":                                "ai.integrate-tmux-bell",
		"notification.go:ensureWSLLegacyAppIDCleaned:set-option":                          "notification.wsl-legacy-cleanup-marker",
		"settings_keybindings.go:regenerateAndReloadTmuxConfig:source-file":               "settings.generated-config-reload",
		"settings_keybindings.go:retireCurrentTmuxKeySequenceState:variable-argv":         "settings.key-sequence-retirement",
		"settings_notifications.go:setDesktopNotifyMode:set-option":                       "settings.desktop-notify-option",
		"settings_appearance.go:setStatusbarDecoration:set-option":                        "settings.statusbar-decoration-option",
		"settings_appearance.go:setAIBadgeStyle:set-option":                               "settings.ai-badge-option",
		"ai.go:applyAIStatusInternalWithActivationPolicy:set-option":                      "agent.presentation",
		"ai.go:setAIPaneBadgeKind:set-option":                                             "agent.presentation",
		"ai.go:resetAINotification:set-option":                                            "agent.presentation",
		"ai.go:runTopic:set-option":                                                       "agent.presentation",
		"ai.go:BindNativeCodexPane:set-option":                                            "agent.presentation",
		"ai.go:configureAIPane:set-option":                                                "agent.presentation",
		"ai.go:configureAIPaneResumeMetadata:set-option":                                  "agent.presentation",
		"ai.go:BindAgentPaneOnRoute:variable-argv":                                        "agent.presentation",
		"ai.go:writeAgentPaneOptionOnRoute:variable-argv":                                 "codex.native-lifecycle-authority",
		"ai.go:bootstrapAIWatchMetadata:set-option":                                       "agent.presentation",
		"ai.go:recordAITopic:set-option":                                                  "agent.presentation",
		"ai.go:projectManagedAgentInteraction:variable-argv":                              "agent.presentation",
		"ai.go:projectManagedAgentTopic:variable-argv":                                    "agent.presentation",
		"ai_ingest.go:recordBellNotification:set-option":                                  "agent.presentation",
		"ai_ingest.go:markAIHookPane:set-option":                                          "agent.presentation",
		"ai_ingest.go:writeAIHookResumeMetadata:set-option":                               "agent.presentation",
		"../integrations/metadata/tmuxmirror.go:RenameProject:set-option":                 "resource.rename-project",
		"../integrations/metadata/tmuxmirror.go:RebindProject:set-option":                 "resource.rebind-project",
		"../integrations/metadata/tmuxmirror.go:writePaneName:set-option":                 "resource.rename-pane",
		"../integrations/metadata/tmuxmirror.go:MirrorPane:set-option":                    "agent.presentation",
		"rebind.go:runProject:helper:RebindProject":                                       "resource.rebind-project",
	}
	// These exact sites are subordinate argv transports reached only from a
	// closed typed producer/controller plan. They are not semantic exemptions:
	// their cited surface must remain planned and their source key is audited
	// bidirectionally just like the app-owned runtimeMutationArgv seam.
	plannedTypedSites := map[string]string{
		"kill.go:KillSession:helper:KillSession":                                                "manual.tagged-kill",
		"resource_reconcile_plan.go:planResourceProjectMirrors:helper:MirrorProject":            "controller.identity",
		"resource_reconcile_plan.go:planResourceProjectMirrors:helper:RebindProject":            "controller.identity",
		"resource_reconcile_plan.go:planResourceBoundMirrorDrift:helper:DisableAutomaticRename": "controller.identity",
		"resource_reconcile_plan.go:planResourceBoundMirrorDrift:set-option":                    "controller.identity",
		"resource_reconcile_plan.go:planExactPaneOption:variable-argv":                          "controller.identity",
		"resource_reconcile_plan.go:planExactManagedPaneOption:set-option":                      "controller.identity",
		"resource_reconcile_plan.go:Run:variable-argv":                                          "controller.identity",
		"project_registry.go:newRegistryReconcilerWithRoute:helper:MirrorPane":                  "controller.identity",
		"../integrations/metadata/tmuxmirror.go:MirrorProject:set-option":                       "controller.identity",
		"../integrations/metadata/tmuxmirror.go:MirrorControlSessionRole:set-option":            "controller.identity",
		"../integrations/metadata/tmuxmirror.go:MirrorWindow:set-option":                        "controller.identity",
		"../integrations/metadata/tmuxmirror.go:disableAutomaticRename:set-option":              "controller.identity",
		"../integrations/metadata/tmuxmirror.go:writeWindowIdentityName:set-option":             "controller.identity",
		"../integrations/metadata/tmuxmirror.go:writeWindowDisplayName:rename-window":           "controller.identity",
	}
	surfaceDispositions := map[string]runtimeMutationSurfaceDisposition{}
	for _, row := range runtimeMutationSurfaces {
		surfaceDispositions[row.ID] = row.Disposition
	}
	seenExemptions := map[string]bool{}
	seenPlannedTypedSites := map[string]bool{}
	seenPlanSites := map[string]bool{}
	for _, name := range files {
		path := filepath.Join(root, name)
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			sourceFunctionName := function.Name.Name
			if name == "../integrations/mux/lifecycle.go" && function.Recv != nil {
				for _, field := range function.Recv.List {
					if receiver, ok := field.Type.(*ast.Ident); ok && receiver.Name == "Runner" {
						sourceFunctionName = "Runner." + function.Name.Name
					}
				}
			}
			argvCallbacks := map[string]bool{}
			if function.Type.Params != nil {
				for _, field := range function.Type.Params.List {
					callback, ok := field.Type.(*ast.FuncType)
					if !ok || callback.Params == nil || len(callback.Params.List) != 1 {
						continue
					}
					variadic, ok := callback.Params.List[0].Type.(*ast.Ellipsis)
					if !ok {
						continue
					}
					name, stringArgs := variadic.Elt.(*ast.Ident)
					if !stringArgs || name.Name != "string" {
						continue
					}
					for _, fieldName := range field.Names {
						argvCallbacks[fieldName.Name] = true
					}
				}
			}
			usesPlanSeam := false
			var rawMutations []string
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if ident, ok := call.Fun.(*ast.Ident); ok {
					switch ident.Name {
					case "executeRuntimeMutationPlan", "runMaterializeMutation", "executeControllerRuntimeMutations":
						usesPlanSeam = true
					}
					if argvCallbacks[ident.Name] {
						for _, argument := range call.Args {
							literal, ok := argument.(*ast.BasicLit)
							if !ok || literal.Kind != token.STRING {
								continue
							}
							value, unquoteErr := strconv.Unquote(literal.Value)
							if unquoteErr == nil && mutating[value] {
								rawMutations = append(rawMutations, value)
							}
						}
					}
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if ok && (selector.Sel.Name == "Command" || selector.Sel.Name == "CommandContext") {
					owner, directExec := selector.X.(*ast.Ident)
					if directExec && owner.Name == "exec" {
						start := 0
						if selector.Sel.Name == "CommandContext" {
							start = 1
						}
						if len(call.Args) > start {
							executable, literalExec := call.Args[start].(*ast.BasicLit)
							name := ""
							if literalExec && executable.Kind == token.STRING {
								name, _ = strconv.Unquote(executable.Value)
							}
							if name == "tmux" {
								before := len(rawMutations)
								variable := false
								for _, argument := range call.Args[start+1:] {
									literal, ok := argument.(*ast.BasicLit)
									if !ok || literal.Kind != token.STRING {
										variable = true
										continue
									}
									value, err := strconv.Unquote(literal.Value)
									if err == nil && mutating[value] {
										rawMutations = append(rawMutations, value)
									}
								}
								if variable && len(rawMutations) == before {
									rawMutations = append(rawMutations, "process-variable-argv")
								}
							}
						}
					}
				}
				if ok && (selector.Sel.Name == "runMutation" || selector.Sel.Name == "runMaterializeMutation" ||
					selector.Sel.Name == "runIdentityWrites" || selector.Sel.Name == "claimRuntimeUIDForRollback" ||
					selector.Sel.Name == "mirrorProject") {
					usesPlanSeam = true
				}
				if ok && (selector.Sel.Name == "run" || selector.Sel.Name == "runCommand") {
					beforeRaw := len(rawMutations)
					directTmux := false
					if len(call.Args) > 0 {
						if executable, literal := call.Args[0].(*ast.BasicLit); literal && executable.Kind == token.STRING {
							name, _ := strconv.Unquote(executable.Value)
							directTmux = name == "tmux"
						}
					}
					for _, argument := range call.Args {
						literal, ok := argument.(*ast.BasicLit)
						if !ok || literal.Kind != token.STRING {
							continue
						}
						value, unquoteErr := strconv.Unquote(literal.Value)
						if unquoteErr == nil && mutating[value] {
							rawMutations = append(rawMutations, value)
						}
					}
					if directTmux && call.Ellipsis.IsValid() && len(rawMutations) == beforeRaw {
						rawMutations = append(rawMutations, "variable-argv")
					}
				}
				if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "executeUnmanagedRuntimeStop" {
					usesPlanSeam = true
				}
				if ok && mutatingHelpers[selector.Sel.Name] {
					registryRename := false
					if receiver, ident := selector.X.(*ast.Ident); ident {
						registryRename = selector.Sel.Name == "RenameWindow" && receiver.Name == "mutator"
					}
					if !registryRename {
						rawMutations = append(rawMutations, "helper:"+selector.Sel.Name)
					}
				}
				if !ok || (selector.Sel.Name != "Run" && selector.Sel.Name != "read") {
					return true
				}
				beforeRaw := len(rawMutations)
				for _, argument := range call.Args {
					literal, ok := argument.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					value, unquoteErr := strconv.Unquote(literal.Value)
					if unquoteErr == nil && mutating[value] {
						rawMutations = append(rawMutations, value)
					}
				}
				if call.Ellipsis.IsValid() && len(rawMutations) == beforeRaw {
					rawMutations = append(rawMutations, "variable-argv")
				}
				return true
			})
			// killCommand is a pure closed argv projection; it cannot execute.
			pureArgvBuilder := name == "materialize.go" && function.Name.Name == "killCommand"
			transportOnly := name == "../integrations/metadata/tmuxmirror.go" && function.Name.Name == "run"
			if len(rawMutations) > 0 && !pureArgvBuilder && !transportOnly {
				for _, verb := range rawMutations {
					key := name + ":" + sourceFunctionName + ":" + verb
					if surfaceID := plannedTypedSites[key]; surfaceID != "" {
						if got := surfaceDispositions[surfaceID]; got != runtimeMutationSurfacePlanned {
							t.Errorf("%s cites product-surface row %q with disposition %q, want planned typed transport", key, surfaceID, got)
						}
						seenPlannedTypedSites[key] = true
						continue
					}
					if surfaceID := exemptRawSites[key]; surfaceID != "" {
						if got := surfaceDispositions[surfaceID]; got != runtimeMutationSurfaceExempt {
							t.Errorf("%s cites product-surface row %q with disposition %q, want exact semantic exemption", key, surfaceID, got)
						}
						seenExemptions[key] = true
						continue
					}
					t.Errorf("%s:%s calls lifecycle/topology tmux verb %q outside the typed execution seam and closed semantic exemptions", name, function.Name.Name, verb)
				}
			}
			key := name + ":" + sourceFunctionName
			if requiredPlanSites[key] {
				seenPlanSites[key] = true
				if !usesPlanSeam {
					t.Errorf("%s does not enter the typed execution seam", key)
				}
			}
		}
	}
	for site := range requiredPlanSites {
		if !seenPlanSites[site] {
			t.Errorf("required plan-only mutation site disappeared without inventory review: %s", site)
		}
	}
	for site := range exemptRawSites {
		if !seenExemptions[site] {
			t.Errorf("classified raw mutation exemption disappeared or changed without inventory review: %s", site)
		}
	}
	for site := range plannedTypedSites {
		if !seenPlannedTypedSites[site] {
			t.Errorf("classified planned typed transport disappeared or changed without inventory review: %s", site)
		}
	}
}

func TestGenericWindowPaneMirrorIsRecorderOnlyOutsideNativePhase2(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root := filepath.Dir(thisFile)
	allowedCalls := map[string]bool{
		"project_registry.go:newRegistryReconcilerWithRoute:MirrorPane": true,
	}
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
			entry.Name() == "create_agent.go" || entry.Name() == "agent_resume.go" {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (selector.Sel.Name != "MirrorWindow" && selector.Sel.Name != "MirrorPane") {
					return true
				}
				key := entry.Name() + ":" + function.Name.Name + ":" + selector.Sel.Name
				if !allowedCalls[key] {
					t.Errorf("generic %s call escaped recorder-only/native closure at %s", selector.Sel.Name, key)
				}
				seen[key] = true
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for key := range allowedCalls {
		if !seen[key] {
			t.Errorf("recorder-only generic mirror call %q disappeared without inventory review", key)
		}
	}
	source, err := os.ReadFile(filepath.Join(root, "project_registry.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if strings.Count(text, "reconciler.mirrorWindow = mirror.MirrorWindow") != 1 ||
		!strings.Contains(text, "if _, recording := runner.(*resourcePlanTmuxRunner); recording {") ||
		strings.Contains(text, "r.mirror.MirrorWindow(ctx") || strings.Contains(text, "r.mirror.MirrorPane(ctx") {
		t.Fatal("project Registry generic Window/Pane mirror is not structurally recorder-only")
	}
}
