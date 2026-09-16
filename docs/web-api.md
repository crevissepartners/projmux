# Web API

Status: **design draft**. Nothing in this document is implemented yet; it is
the contract `projmux web` is built against. Sections marked *Open* are
decisions still to be made.

`projmux web` serves one HTTP API and the browser client that uses it. The
server runs in the projmux process and calls the same internal code the CLI
does. It does not run the `projmux` binary, parse CLI text, or read state
files behind projmux's back.

## Listeners

One handler is served on two listeners:

| listener | default | who it is for |
| --- | --- | --- |
| TCP | `127.0.0.1:8787` (`--addr`) | the browser |
| unix socket | `<StateDir>/web/api.sock`, mode `0600` (`--socket`) | local programs |

Both reach the same routes with the same checks. The TCP listener binds
loopback by default. Binding anything else is refused unless `--addr` is given
explicitly, and even then there is no authentication (see *Out of scope*).

A local HTTP server is reachable from any web page the operator has open, so
the TCP listener also enforces these checks:

- **Host.** The `Host` header must be `127.0.0.1:<port>`, `localhost:<port>`,
  or `[::1]:<port>`. This blocks DNS-rebinding pages.
- **Origin.** A request that carries `Origin` must name the server's own
  origin. A cross-site `fetch` or form post is refused before any handler runs.
- **Content type.** Every non-`GET` request must be
  `Content-Type: application/json`. HTML forms cannot send that without CORS
  preflight, and the server answers no preflight.

The unix socket skips the Host and Origin checks. Its file mode is the access
control.

## Process model

- **The server environment is scrubbed.** `TMUX` and `TMUX_PANE` are removed
  before any handler runs. The CLI uses them to pick implicit targets, the
  default message source, and delete self-targets. A server started from a
  tmux pane would otherwise scope every request to that pane. Every route
  names its targets explicitly, and tmux is reached through the app socket
  (`-L projmux`).
- **Commands are built per request.** `internal/app` command objects cache
  observations for their own lifetime. A long-lived server that reused them
  would serve stale runtime state.
- **Mutations are serialized.** One mutation runs at a time. Reads do not wait
  for mutations.

## Conventions

- **Resources are the Registry's own shapes.** Items are `projmux.io/v1alpha1`
  objects, exactly as `projmux get <kind> -o json` prints them (`metadata`,
  `spec`, `status`, `context`). Lists use the same `<Kind>List` envelope.
- **References are uids.** Path segments are uids (`proj-…`, `win-…`,
  `pane-…`, `agent-…`). Names are not accepted in paths, because names can
  change and can be ambiguous.
- **Nesting is checked.** A nested path must describe the real ownership
  chain. A Window that belongs to another Project is `404 not-found`, not a
  silent answer under the wrong parent. An Agent-owned Pane belongs to the
  Window of the Agent that owns it.
- **Mutations are named, and the request body never supplies refs.** Every
  uid a mutation acts on comes from the path and is resolved in the Registry.
  Body fields carry only values such as a name, a provider, or text.
- **Destructive and quota-spending mutations need `"confirm": true`.** This
  applies to create window, create agent, resume, and both deletes. The flag
  makes a bare request inert; it is not a UI confirmation.
- **Placement is fixed to `right`.** Create agent has no placement field.

### Errors

Every non-2xx response has one shape:

```json
{
  "error": {
    "code": "turn-in-progress",
    "message": "exact thread already has a turn in progress",
    "status": 409,
    "details": {}
  }
}
```

`code` is a stable token that clients branch on. `message` is for people and
may change. Where projmux already has a machine token, that token is `code`
verbatim:

| source | codes |
| --- | --- |
| Codex control (`refusedControl`) | `stale-epoch`, `stale-binding`, `unavailable`, `stale-turn`, `turn-state-unavailable`, `turn-in-progress`, `protocol-error`, `no-active-turn`, `invalid-operation`, `ambiguous-request`, `unsafe-decision`, `timeout` |
| focus result `reason` | `session-unresolved`, `no-attached-client`, `pane-id-unresolved`, … |
| message delivery | `delivery.state` / `delivery.reason` in `details` |

The server adds its own codes:

| code | status | meaning |
| --- | --- | --- |
| `invalid-request` | 400 | malformed body, unknown field value, or empty text |
| `confirm-required` | 400 | a confirm-gated mutation without `"confirm": true` |
| `forbidden-origin` | 403 | the Host, Origin, or Content-Type check failed |
| `not-found` | 404 | no such uid, or the uid is not under the given parent |
| `name-conflict` | 409 | the name is already used in that scope |
| `invalid-name` | 400 | the name fails metadata validation |
| `not-live` | 409 | the target has no live runtime to act on |
| `unsupported` | 400 | the provider has no such surface (for example, a turn on Claude) |
| `refused` | 409 | projmux refused, and no finer token exists yet |
| `internal` | 500 | anything else |

Mapping CLI refusals that are only strings today, such as delete cascade
drift, into finer codes is follow-up work. They start as `refused`, with the
projmux message intact.

A refusal is still an HTTP error. It is not a `200` with `ok: false`. Clients
branch on `code`.

### Mutation results

A mutation that has an `OperationReceipt` returns it (`apiVersion
"OperationReceipt/v1"`). A mutation that creates a resource also returns that
resource:

```json
{ "receipt": { "apiVersion": "OperationReceipt/v1", "operation": "create.agent", … },
  "agent": { "kind": "Agent", "metadata": { "uid": "agent-…" }, … },
  "pane":  { "kind": "Pane",  "metadata": { "uid": "pane-…" }, … } }
```

## Core routes

All core routes are under `/api/v1`.

| method | path | CLI equivalent | notes |
| --- | --- | --- | --- |
| GET | `/api/v1/version` | `projmux version` | also a liveness probe |
| GET | `/api/v1/graph` | — | one Registry read plus one tmux observation: every Project, Window, Pane and Agent with live status |
| GET | `/api/v1/projects` | `get projects -o json` | `ProjectList` |
| GET | `/api/v1/projects/{project}` | `get project uid:…` | |
| GET | `/api/v1/projects/{project}/windows` | `get windows -p uid:…` | `WindowList` |
| POST | `/api/v1/projects/{project}/windows` | `create window` | body `{name?, agent?: {provider, payload?}, focus?, confirm}`; `agent` also starts that provider in the new Window |
| GET | `/api/v1/projects/{project}/windows/{window}` | `get window` | |
| PATCH | `/api/v1/projects/{project}/windows/{window}` | `rename window` | body `{name}` |
| DELETE | `/api/v1/projects/{project}/windows/{window}` | `delete window --yes` | body `{confirm}`; `?dryRun=true` returns the cascade plan and deletes nothing |
| GET | `/api/v1/projects/{project}/windows/{window}/panes` | `get panes` | `PaneList` |
| GET | `/api/v1/projects/{project}/windows/{window}/panes/{pane}` | `get pane` | |
| PATCH | `/api/v1/projects/{project}/windows/{window}/panes/{pane}` | `rename pane` | body `{name}` |
| DELETE | `/api/v1/projects/{project}/windows/{window}/panes/{pane}` | `delete pane --yes` | body `{confirm}` |
| POST | `/api/v1/projects/{project}/windows/{window}/panes/{pane}/focus` | `internal focus` | moves the operator's attached client |
| GET | `/api/v1/projects/{project}/windows/{window}/agents` | `get agents` | includes Offline agents the Window owns, which are the resume candidates |
| POST | `/api/v1/projects/{project}/windows/{window}/agents` | `create agent` | body `{provider, anchorPane?, cwdFrom?, payload?, confirm}`; `anchorPane` must be a pane uid in this Window |
| GET | `/api/v1/agents/{agent}` | `get agent` | agent uids are global, so this route is flat |
| PATCH | `/api/v1/agents/{agent}` | `rename agent` | body `{name}` |
| POST | `/api/v1/agents/{agent}/resume` | `agent resume` | body `{confirm}` |
| GET | `/api/v1/agents/{agent}/capabilities` | `agent capabilities uid:…` | |
| POST | `/api/v1/agents/{agent}/turns` | `agent turn start` | body `{text}`; Codex only |
| POST | `/api/v1/agents/{agent}/turns/current/steer` | `agent turn steer` | body `{text}` |
| DELETE | `/api/v1/agents/{agent}/turns/current` | `agent turn interrupt` | |
| POST | `/api/v1/agents/{agent}/messages` | `agent message send` | body `{source, body, messageRef?, replyTo?, ttl?}`; returns the delivery receipt |
| GET | `/api/v1/notifications` | `notify list --json` | |
| POST | `/api/v1/notifications/{id}/ack` | `notification ack` | also publishes a queue refresh, which the CLI ack does not |
| GET | `/api/v1/usage` | `agent usage --json` | cached snapshots only; never collects |
| GET | `/api/v1/system` | status bar CPU and MEM | |

Starting a turn does not fall back to steer on the server. A client that gets
`turn-in-progress` decides whether to steer, so one request never becomes two
different operations.

### Events

`GET /api/v1/events?topics=graph,notifications,usage,system` is one
server-sent-events stream. The event names are fixed:

| event | payload | sent when |
| --- | --- | --- |
| `graph` | the same body as `GET /api/v1/graph` | the Registry file identity changes, or tmux topology changes, and the body differs from the last one sent |
| `notifications` | the same body as `GET /api/v1/notifications` | the notify queue publishes a refresh or its file changes |
| `usage` | the same body as `GET /api/v1/usage` | the snapshot file changes |
| `system` | the same body as `GET /api/v1/system` | on a fixed tick, only when a value changes |
| `error` | the error envelope | a read failed; the stream stays open |

The first frame of each topic is sent at once. After that, a frame is sent
only on change. A `: keepalive` comment follows 20 seconds of silence.

## Web routes

These routes exist for the browser client. They show the terminal, not the
Registry, so they are kept out of the core surface. They live under
`/api/v1/web` and follow the same conventions and error format.

| method | path | what it is |
| --- | --- | --- |
| GET | `/api/v1/web/i18n` | the `web.*` catalog for the resolved locale |
| GET | `/api/v1/web/panes/{pane}/screen` | one `capture-pane -e` of the pane, parsed into styled runs |
| GET | `/api/v1/web/panes/{pane}/screen/events` | SSE `screen` frames, sent on change |
| GET | `/api/v1/web/windows/{window}/layout` | pane geometry in cells (`?contents=1` adds screens) |
| GET | `/api/v1/web/windows/{window}/layout/events` | SSE `layout` frames, sent on change |
| GET | `/api/v1/web/agents/{agent}/transcript` | the provider transcript flattened to turns (`?limit=`) |
| GET | `/api/v1/web/agents/{agent}/transcript/events` | SSE `turn` and `error` frames from the end of the file (`?from=start` to replay) |
| GET | `/api/v1/web/agents/{agent}/repository` | `{web, root, rev}` for linking references in the transcript; GitHub origins only |
| GET | `/api/v1/web/windows/{window}/resume-candidates` | the Window's Offline agents, each with its first and last transcript line |

Web routes take a bare uid, because uids are global. Each route resolves the
uid in the Registry and refuses a target that is not on the app-owned tmux
server.

### No keys into a Pane

The prototype answered AskUserQuestion by pressing the widget's keys, and
could paste text into a Pane so it arrived as the operator's own words. Both
are left out. projmux keeps agent input off raw pane keys, and
`agent_message_test.go` enforces that for the message path; the web client
does not get an exception. Where the prototype offered those inputs, the
client shows that they are done in the terminal.

## Open

- **Operator input.** Three gaps are one problem: text or an answer a person
  gives in the browser has to reach an Agent as the operator's input.
  - *Source.* A browser has no Pane, and `agent message send` needs a source
    Agent. Until projmux has a source kind for an operator, the client anchors
    on the target Agent itself and signs `messageRef` with the prefix
    `projmux-web-`. The envelope then names an Agent as the sender of text a
    person typed.
  - *Delivery.* The coordination broker always wraps a message in an
    envelope marked untrusted. There is no path that delivers it as the
    operator's own utterance.
  - *Questions.* There is no known channel for answering a Claude
    AskUserQuestion from outside the terminal. Finding out whether one exists
    comes first.

  Codex is already covered: the app-server takes a turn as a user turn. The
  design is written up separately and agreed before anything is built.
- **Source maps.** Whether the built client's source maps are committed next
  to it depends on their size.
- **CI for the built client.** `make web-check` rebuilds the client and fails
  if the result differs from the committed files. Which CI job runs it must be
  decided without renaming any required job.

## Out of scope

- Authentication, remote access, and multiple users.
- A delivery mode that removes the coordination envelope.
- A resident daemon or a versioned socket protocol beyond this HTTP API. They
  come when there is a second client that needs them.

## Layout

```text
internal/web/            HTTP handlers, SSE, error envelope, listeners (package web)
internal/web/dist/       built client, embedded with go:embed (generated, committed)
internal/web/_ui/        client source: Vite + Svelte 5 + TypeScript
internal/app/web*.go     the backend web calls into: resource reads and mutations
```

`internal/web` depends on an interface that `internal/app` implements, so the
HTTP layer never reaches into command internals. The client source directory
starts with `_`, so `go ./...` never walks it or its `node_modules`.
