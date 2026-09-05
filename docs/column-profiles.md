# Column profiles

Plural Registry reads (`get projects|windows|panes|agents`) now default to
`NAME STATUS ACTIONS`. The route already selects the resource kind. Runtime
reads default to their identity, containment, and classification columns. Use `-o wide` (or `--output wide`) to print the full
column profile. Terminal width and data length never select a profile.

Removing KIND changes these four default tables from four columns to three and
moves NAME, STATUS and ACTIONS one position left. Existing positional parsers
can break; request `-o wide` or recover each resource's `kind` from `-o json`.
Mixed Registry/Runtime pickers keep KIND in both profiles.

Scripts that need diagnostic columns must request `-o wide` or a structured projection explicitly. Registry JSON items
retain `context.value`, `context.source`, and `context.observed`; `describe` keeps
resource and termination detail. Existing JSON, metadata, uid, name, ref and none
projections retain their contracts, as do singular reads and selector cardinality.
`wide` is accepted only on the seven plural columnar read routes.

| Surface / kind | Default | Wide |
| --- | --- | --- |
| Registry Project | NAME STATUS ACTIONS | KIND NAME STATUS ACTIONS CONTEXT SOURCE OBSERVED AGE |
| Registry Window | NAME STATUS ACTIONS | KIND NAME STATUS ACTIONS CONTEXT SOURCE OBSERVED PROJECT AGE |
| Registry Pane | NAME STATUS ACTIONS | KIND NAME STATUS ACTIONS CONTEXT SOURCE OBSERVED PROJECT WINDOW AGENT TERMINATION AGE |
| Registry Agent | NAME STATUS ACTIONS | KIND NAME STATUS ACTIONS CONTEXT SOURCE OBSERVED INTERACTION PROJECT WINDOW SESSION TERMINATION AGE |
| Runtime Session | SESSION NAME CLASS | SESSION NAME CLASS UID RESOURCE REASON |
| Runtime Window | WINDOW SESSION NAME CLASS | WINDOW SESSION NAME CLASS UID RESOURCE REASON |
| Runtime Pane | PANE WINDOW TITLE CLASS | PANE WINDOW TITLE CLASS UID RESOURCE REASON |

Registry defaults omit KIND, CONTEXT, SOURCE, OBSERVED, AGE and each kind's owner-chain,
INTERACTION, SESSION and TERMINATION diagnostics shown in the wide column above.
Runtime defaults omit UID, RESOURCE and REASON. Wide retains their full values;
stdout is unbounded and copyable, including long names, context, provider session
references and diagnostic reasons. Free text may contain spaces: structured JSON
is the recovery surface for machine consumers of those values. An empty resource
list still emits no bytes, while an empty runtime report retains its host and
availability header.

ACTIONS comes from the existing Registry navigation projector over the same
invocation graph used for context and runtime observation. It adds no tmux
observation or Registry write. Eligible actions retain their existing order;
ControlSession-owned rows retain their existing empty action set (`-` in the
CLI). NAME remains the durable Registry address; CONTEXT remains presentation.

The Registry and Runtime diagnostics pickers consume the same typed catalog in
`internal/app/column_profiles.go` and open with compact columns:

| Picker | Compact default | Explicit wide |
| --- | --- | --- |
| Registry mixed | KIND NAME STATUS ACTIONS | KIND NAME STATUS PROGRESS TERMINATION ACTIONS RUNTIME UID |
| Runtime mixed | KIND ID IN NAME CLASS | KIND ID IN NAME CLASS RESOURCE REASON |

Press **Alt-W** to toggle wide columns and press it again to return to compact.
The footer shows the effective key and the next projection. The query, selected
row, row order, full search values and current observation survive each toggle.
The choice lasts for this open picker, including visits to a row's action menu;
closing and reopening starts compact. Toggling writes no Registry or config.

Settings > Keybindings > Sidebar & picker actions exposes **Registry Inspector**
and **Runtime Diagnostics**, each with **Toggle compact / wide columns**. Their
canonical IDs are `resource-inspector.columns.toggle` and
`runtime-diagnostics.columns.toggle`. Existing single-key overrides are supported;
there is no saved/global column profile or picker-local key sequence.

Compact Registry NAME keeps the full invocation context (or the full durable name
when context is empty), and omits the repeated provider/phase and role adornments
that wide NAME retains. A full-UID/Hangul hierarchy with the existing action lists
fits the 75-cell content budget at 80 columns without cutting NAME. Compact uses
one separating cell per column; a previously clipped 76-cell regression row now
uses 73 cells with all values intact. Wide retains two separating cells. The picker
adds no NAME, RESOURCE, RUNTIME or REASON truncation bounds. Wide keeps the catalog
field order and full projected values, including the existing PROGRESS producer's
own policy.

Wide rows can be clipped by the fixed viewport. The representative 142-cell
Registry row clips at 80/120/180-column clients and fits from a 184-column client
with the production 80%-width borderless popup. The native frame, scrollbar gutter
and pointer reserve five cells: a 180-column client gives 139 label cells, and a
184-column client gives 142. Actual action lists, runtime targets and free text can make rows wider,
so no fixed client width guarantees all wide values. Enter opens the existing
row action menu with each wide field on a separate detail row. JSON/`describe`
and CLI `-o wide` provide recovery for values that still do not fit. There is no
horizontal navigation or pagination; footer overflow and provider behavior are
unchanged.
