package app

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// columnField identifies a semantic value independently of its header alias.
// Runtime handles and Registry references deliberately have distinct IDs.
type columnField string

const (
	columnKind        columnField = "kind"
	columnName        columnField = "name"
	columnStatus      columnField = "status"
	columnActions     columnField = "actions"
	columnContext     columnField = "context"
	columnSource      columnField = "context-source"
	columnObserved    columnField = "context-observed"
	columnAge         columnField = "age"
	columnProject     columnField = "project"
	columnWindow      columnField = "window"
	columnAgent       columnField = "agent"
	columnTermination columnField = "termination"
	columnInteraction columnField = "interaction"
	columnSession     columnField = "session"
	columnProgress    columnField = "progress"
	columnRuntime     columnField = "runtime"
	columnUID         columnField = "uid"
	columnID          columnField = "runtime-id"
	columnContainer   columnField = "runtime-container"
	columnClass       columnField = "class"
	columnResource    columnField = "resource"
	columnReason      columnField = "reason"
)

type columnSurface string

const (
	columnResourceCLI    columnSurface = "resource-cli"
	columnRegistryPicker columnSurface = "registry-picker"
	columnRuntimeCLI     columnSurface = "runtime-cli"
	columnRuntimePicker  columnSurface = "runtime-picker"
)

type columnProfile string

const (
	columnCompact columnProfile = "compact"
	columnWide    columnProfile = "wide"
)

type columnSurfaceKey struct {
	surface columnSurface
	kind    string
}
type columnSpec struct {
	field   columnField
	header  string
	compact bool
}

// columnCatalog is the only authority for field capabilities, header aliases,
// and profile order. Presence declares capability on that surface and kind;
// wide follows slice order and compact is its ordered subset. Width and data
// never select a profile. Picker consumers explicitly retain wide until their
// separate default/control cutover.
var columnCatalog = map[columnSurfaceKey][]columnSpec{
	{columnResourceCLI, string(coremetadata.KindProject)}: {
		{columnKind, "KIND", true},
		{columnName, "NAME", true},
		{columnStatus, "STATUS", true},
		{columnActions, "ACTIONS", true},
		{columnContext, "CONTEXT", false},
		{columnSource, "SOURCE", false},
		{columnObserved, "OBSERVED", false},
		{columnAge, "AGE", false},
	},
	{columnResourceCLI, string(coremetadata.KindWindow)}: {
		{columnKind, "KIND", true},
		{columnName, "NAME", true},
		{columnStatus, "STATUS", true},
		{columnActions, "ACTIONS", true},
		{columnContext, "CONTEXT", false},
		{columnSource, "SOURCE", false},
		{columnObserved, "OBSERVED", false},
		{columnProject, "PROJECT", false},
		{columnAge, "AGE", false},
	},
	{columnResourceCLI, string(coremetadata.KindPane)}: {
		{columnKind, "KIND", true},
		{columnName, "NAME", true},
		{columnStatus, "STATUS", true},
		{columnActions, "ACTIONS", true},
		{columnContext, "CONTEXT", false},
		{columnSource, "SOURCE", false},
		{columnObserved, "OBSERVED", false},
		{columnProject, "PROJECT", false},
		{columnWindow, "WINDOW", false},
		{columnAgent, "AGENT", false},
		{columnTermination, "TERMINATION", false},
		{columnAge, "AGE", false},
	},
	{columnResourceCLI, string(coremetadata.KindAgent)}: {
		{columnKind, "KIND", true},
		{columnName, "NAME", true},
		{columnStatus, "STATUS", true},
		{columnActions, "ACTIONS", true},
		{columnContext, "CONTEXT", false},
		{columnSource, "SOURCE", false},
		{columnObserved, "OBSERVED", false},
		{columnInteraction, "INTERACTION", false},
		{columnProject, "PROJECT", false},
		{columnWindow, "WINDOW", false},
		{columnSession, "SESSION", false},
		{columnTermination, "TERMINATION", false},
		{columnAge, "AGE", false},
	},
	{columnRegistryPicker, ""}: {
		{columnKind, "KIND", true},
		{columnName, "NAME", true},
		{columnStatus, "STATUS", true},
		{columnProgress, "PROGRESS", false},
		{columnTermination, "TERMINATION", false},
		{columnActions, "ACTIONS", true},
		{columnRuntime, "RUNTIME", false},
		{columnUID, "UID", false},
	},
	{columnRuntimeCLI, string(resourcegraph.ObjectSession)}: {
		{columnID, "SESSION", true},
		{columnName, "NAME", true},
		{columnClass, "CLASS", true},
		{columnUID, "UID", false},
		{columnResource, "RESOURCE", false},
		{columnReason, "REASON", false},
	},
	{columnRuntimeCLI, string(resourcegraph.ObjectWindow)}: {
		{columnID, "WINDOW", true},
		{columnContainer, "SESSION", true},
		{columnName, "NAME", true},
		{columnClass, "CLASS", true},
		{columnUID, "UID", false},
		{columnResource, "RESOURCE", false},
		{columnReason, "REASON", false},
	},
	{columnRuntimeCLI, string(resourcegraph.ObjectPane)}: {
		{columnID, "PANE", true},
		{columnContainer, "WINDOW", true},
		{columnName, "TITLE", true},
		{columnClass, "CLASS", true},
		{columnUID, "UID", false},
		{columnResource, "RESOURCE", false},
		{columnReason, "REASON", false},
	},
	{columnRuntimePicker, ""}: {
		{columnKind, "KIND", true},
		{columnID, "ID", true},
		{columnContainer, "IN", true},
		{columnName, "NAME", true},
		{columnClass, "CLASS", true},
		{columnResource, "RESOURCE", false},
		{columnReason, "REASON", false},
	},
}

func columnsFor(surface columnSurface, kind string, profile columnProfile) []columnSpec {
	if profile != columnCompact && profile != columnWide {
		return nil
	}
	var out []columnSpec
	for _, column := range columnCatalog[columnSurfaceKey{surface, kind}] {
		if profile == columnWide || column.compact {
			out = append(out, column)
		}
	}
	return out
}
func columnHeaders(columns []columnSpec) []string {
	out := make([]string, len(columns))
	for i, column := range columns {
		out[i] = column.header
	}
	return out
}
func columnValues(columns []columnSpec, value func(columnField) string) []string {
	out := make([]string, len(columns))
	for i, column := range columns {
		out[i] = value(column.field)
	}
	return out
}
