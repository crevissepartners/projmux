package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/cli"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/runtimediag"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// The Runtime diagnostics escape hatch: one read seam shared by the public
// `get runtime` route and the diagnostics picker.
//
// It exists because a Registry-first surface is, correctly, not an inventory. A
// Project that was never registered, an operator's own shell, the Home control
// session, a scratch session -- none of them are managed resources, so none of
// them appear where managed rows are listed, and there is otherwise no place
// left that shows an operator what is actually on the server. The escape hatch
// is that place, and it deliberately has no authority: it reads, it names, and
// it hands the existing focus/attach/inspect routes an exact handle.
//
// Three properties are contractual and every consumer inherits them:
//
//   - One exact host. The transport is an explicit `--socket`/`--socket-path`
//     or the inherited `$TMUX` socket path, and nothing else. There is no
//     default-server probe and no second socket, so a report about one server
//     can never contain another server's objects.
//   - No transport is an answer. Outside tmux with no socket flag the read
//     succeeds and reports every scope unavailable with a stated reason, rather
//     than failing or guessing. `reconcile resources` refuses that case because
//     it is about to write; a read has nothing to protect.
//   - Zero writes. The Registry is opened read-only, the observation is the
//     bounded four-query adapter that owns no write verb, and the projection is
//     pure. A refresh of this surface is indistinguishable, from the machine's
//     point of view, from not having run it.

// runtimeDiagnosticsReader resolves one exact host into a resolved graph.
type runtimeDiagnosticsReader struct {
	runner    tmuxCommandRunner
	lookupEnv func(string) string
	// loadRegistry is the read-only Registry load. It never creates state: the
	// escape hatch must be usable on a machine whose Registry is the thing the
	// operator is trying to understand.
	loadRegistry func() (coremetadata.Registry, error)
	// observe is injectable so a test can state a machine state instead of
	// scripting tmux output.
	observe func(ctx context.Context, transport resourcegraph.Transport) resourcegraph.Inventory
}

func newRuntimeDiagnosticsReader(runner tmuxCommandRunner) *runtimeDiagnosticsReader {
	if runner == nil {
		runner = inttmux.ExecRunner{}
	}
	return &runtimeDiagnosticsReader{
		runner:       runner,
		lookupEnv:    os.Getenv,
		loadRegistry: loadResourceRegistry,
		observe: func(ctx context.Context, transport resourcegraph.Transport) resourcegraph.Inventory {
			return intmetadata.NewInventoryObserver(runner, transport).Observe(ctx)
		},
	}
}

// runtimeTransportRequest is the raw routing input of one runtime read.
type runtimeTransportRequest struct {
	socket     string
	socketPath string
}

// transport resolves the exact routing of this invocation.
//
// Precedence is the graph package's: explicit flags first, the inherited $TMUX
// socket path second, nothing third. Resolving it here rather than in each
// caller is what keeps the picker and the read route pointed at the same
// server for the same invocation.
func (r *runtimeDiagnosticsReader) transport(req runtimeTransportRequest) (resourcegraph.Transport, error) {
	inherited := ""
	if r.lookupEnv != nil {
		inherited = r.lookupEnv("TMUX")
	}
	return resourcegraph.ResolveTransport(resourcegraph.TransportRequest{
		SocketName:    req.socket,
		SocketPath:    req.socketPath,
		InheritedTMUX: inherited,
	})
}

// resolve takes one observation of the exact host and joins it to the Registry.
func (r *runtimeDiagnosticsReader) resolve(ctx context.Context, transport resourcegraph.Transport) (resourcegraph.Graph, error) {
	if r.loadRegistry == nil {
		return resourcegraph.Graph{}, errors.New("runtime diagnostics registry reader is not configured")
	}
	registry, err := r.loadRegistry()
	if err != nil {
		return resourcegraph.Graph{}, MapMetadataError(err)
	}
	if r.observe == nil {
		return resourcegraph.Graph{}, errors.New("runtime diagnostics observer is not configured")
	}
	return resourcegraph.Resolve(registry, r.observe(ctx, transport)), nil
}

// socketPath asks the observed server for its own `#{socket_path}`.
//
// It is the one query this surface makes outside the bounded inventory, and it
// exists for the safe actions rather than for the read: `focus --socket` takes
// an absolute path, so a `-L <name>` transport has to be turned into the path
// tmux itself reports before a handle can be handed over. A server that is not
// running answers nothing, and no action is offered.
func (r *runtimeDiagnosticsReader) socketPath(ctx context.Context, transport resourcegraph.Transport) (string, bool) {
	if r.runner == nil || !transport.Present() {
		return "", false
	}
	if transport.Kind == resourcegraph.TransportSocketPath {
		return transport.Value, true
	}
	args := append(transport.Args(), "display-message", "-p", "#{socket_path}")
	out, err := r.runner.Run(ctx, "tmux", args...)
	if err != nil {
		return "", false
	}
	path := strings.TrimSpace(string(out))
	return path, path != ""
}

// runtimeKindTokens maps the accepted `get runtime <kind>` tokens onto the
// closed tmux object kinds. It is the only place the two vocabularies meet.
var runtimeKindTokens = map[string]resourcegraph.ObjectKind{
	"sessions": resourcegraph.ObjectSession,
	"windows":  resourcegraph.ObjectWindow,
	"panes":    resourcegraph.ObjectPane,
}

// runtimeTableColumns consumes the catalog's compact CLI profile.
func runtimeTableColumns(kind resourcegraph.ObjectKind, profile columnProfile) []columnSpec {
	return columnsFor(columnRuntimeCLI, string(kind), profile)
}

func runtimeTableRow(kind resourcegraph.ObjectKind, row runtimediag.Row, profile columnProfile) []string {
	return columnValues(runtimeTableColumns(kind, profile), func(field columnField) string {
		return runtimeColumnValue(field, row, false)
	})
}
func runtimeColumnValue(field columnField, row runtimediag.Row, picker bool) string {
	switch field {
	case columnKind:
		return runtimeCell(row.Kind)
	case columnID:
		return runtimeCell(row.ID)
	case columnContainer:
		if !picker && row.Kind == string(resourcegraph.ObjectWindow) {
			return runtimeCell(row.SessionID)
		}
		return runtimeCell(row.ContainerID)
	case columnName:
		return runtimeCell(row.Name)
	case columnClass:
		return runtimeCell(row.Class)
	case columnUID:
		return runtimeCell(row.UID)
	case columnResource:
		return runtimeResourceCell(row)
	case columnReason:
		return runtimeCell(row.Reason)
	}
	return ""
}

// runtimeCell renders one column value, using the same `-` placeholder the
// resource tables use so an empty column is visibly empty rather than a gap
// that shifts the eye onto the next column's value.
func runtimeCell(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

func runtimeResourceCell(row runtimediag.Row) string {
	if row.Resource == nil {
		return "-"
	}
	name := strings.TrimSpace(row.Resource.Name)
	kind := strings.TrimSpace(row.Resource.Kind)
	switch {
	case kind != "" && name != "":
		return kind + "/" + name
	case name != "":
		return name
	case kind != "":
		return kind
	default:
		return runtimeCell(row.Resource.UID)
	}
}

// writeRuntimeReport renders one report through the human or JSON projection.
//
// The human projection always writes the header block, even when the item list
// is empty. That is the opposite of the resource tables, and deliberately so: a
// resource read with no matches is a query that matched nothing, while a
// runtime read with no rows is a claim about a machine, and "no sessions" is
// only trustworthy next to which server was asked and whether the answer could
// be taken at all.
func writeRuntimeReport(stdout io.Writer, report runtimediag.Report, mode cli.OutputMode) error {
	if mode == cli.OutputModeJSON {
		return writeJSON(stdout, report)
	}
	if _, err := fmt.Fprintln(stdout, runtimeHeaderLine(report)); err != nil {
		return err
	}
	for _, entry := range report.Unavailable {
		if _, err := fmt.Fprintf(stdout, "unavailable %s: %s\n", entry.Scope, entry.Reason); err != nil {
			return err
		}
	}
	if len(report.Items) == 0 {
		return nil
	}
	kind := runtimeObjectKindOfReport(report)
	profile := columnCompact
	if mode == cli.OutputModeWide {
		profile = columnWide
	}
	headers := columnHeaders(runtimeTableColumns(kind, profile))
	if len(headers) == 0 {
		return fmt.Errorf("runtime diagnostics: no column contract is declared for report kind %q", report.Kind)
	}
	rows := make([][]string, 0, len(report.Items)+1)
	rows = append(rows, headers)
	for _, item := range report.Items {
		rows = append(rows, runtimeTableRow(kind, item, profile))
	}
	widths := make([]int, len(headers))
	for _, row := range rows {
		for i, cell := range row {
			if width := resourceCellWidth(cell); width > widths[i] {
				widths[i] = width
			}
		}
	}
	for _, row := range rows {
		if _, err := fmt.Fprintln(stdout, resourceTableLine(row, widths)); err != nil {
			return err
		}
	}
	return nil
}

// runtimeObjectKindOfReport maps a published report kind back onto the tmux
// object kind that owns its columns.
func runtimeObjectKindOfReport(report runtimediag.Report) resourcegraph.ObjectKind {
	for _, kind := range resourcegraph.ObjectKinds() {
		if runtimediag.ListKind(kind) == report.Kind {
			return kind
		}
	}
	return ""
}

// runtimeHeaderLine states which server answered and how it is owned.
//
// Both halves are load bearing. The transport is what proves no other socket
// was consulted, and the host mode is what explains an attribution: the same
// unmarked pane is `unattributed` on a server projmux started and `foreign` on
// the operator's own, and without the host on the line that difference reads as
// an inconsistency.
func runtimeHeaderLine(report runtimediag.Report) string {
	transport := "no tmux transport"
	switch report.Transport.Kind {
	case string(resourcegraph.TransportSocketName):
		transport = "tmux -L " + report.Transport.Value
	case string(resourcegraph.TransportSocketPath):
		transport = "tmux -S " + report.Transport.Value
	}
	return fmt.Sprintf("host %s  transport %s  source %s",
		runtimeCell(report.HostMode), transport, runtimeCell(report.Transport.Source))
}
