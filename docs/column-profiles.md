# Column profiles

Plural Registry reads (`get projects|windows|panes|agents`) now default to
`KIND NAME STATUS ACTIONS`. Runtime reads default to their identity, containment,
and classification columns. Use `-o wide` (or `--output wide`) to print the full
column profile. Terminal width and data length never select a profile.

This is a breaking default stdout change. Scripts that need diagnostic columns
must request `-o wide` or a structured projection explicitly. Registry JSON items
retain `context.value`, `context.source`, and `context.observed`; `describe` keeps
resource and termination detail. Existing JSON, metadata, uid, name, ref and none
projections retain their contracts, as do singular reads and selector cardinality.
`wide` is accepted only on the seven plural columnar read routes.

| Surface / kind | Default | Wide |
| --- | --- | --- |
| Registry Project | KIND NAME STATUS ACTIONS | KIND NAME STATUS ACTIONS CONTEXT SOURCE OBSERVED AGE |
| Registry Window | KIND NAME STATUS ACTIONS | KIND NAME STATUS ACTIONS CONTEXT SOURCE OBSERVED PROJECT AGE |
| Registry Pane | KIND NAME STATUS ACTIONS | KIND NAME STATUS ACTIONS CONTEXT SOURCE OBSERVED PROJECT WINDOW AGENT TERMINATION AGE |
| Registry Agent | KIND NAME STATUS ACTIONS | KIND NAME STATUS ACTIONS CONTEXT SOURCE OBSERVED INTERACTION PROJECT WINDOW SESSION TERMINATION AGE |
| Runtime Session | SESSION NAME CLASS | SESSION NAME CLASS UID RESOURCE REASON |
| Runtime Window | WINDOW SESSION NAME CLASS | WINDOW SESSION NAME CLASS UID RESOURCE REASON |
| Runtime Pane | PANE WINDOW TITLE CLASS | PANE WINDOW TITLE CLASS UID RESOURCE REASON |

Registry defaults omit CONTEXT, SOURCE, OBSERVED, AGE and each kind's owner-chain,
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
`internal/app/column_profiles.go`. This cutover preserves their existing wide
first frame, width bounds, controls, row identity, ordering and search keys:

| Picker | Compact catalog profile (reserved) | Current wide default |
| --- | --- | --- |
| Registry mixed | KIND NAME STATUS ACTIONS | KIND NAME STATUS PROGRESS TERMINATION ACTIONS RUNTIME UID |
| Runtime mixed | KIND ID IN NAME CLASS | KIND ID IN NAME CLASS RESOURCE REASON |

Picker compact defaults, local toggles, viewport clipping and removal of the
existing NAME/RESOURCE bounds belong to the subsequent picker phase. No Settings,
footer or provider elapsed behavior changes here.
