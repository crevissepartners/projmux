# Web API

Status: **in progress** on the `feat/projmux-web` branch. This is the
contract `projmux web` is built against. Sections marked *Open* are decisions
still to be made.

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

Both reach the same routes. The TCP listener only binds loopback: any other
`--addr` is refused, because the API is plain http and its start token would
cross the network in cleartext.

A local HTTP server is reachable from any web page the operator has open, so
the TCP listener enforces these checks, in this order:

- **Host.** The `Host` header must be `127.0.0.1:<port>`, `localhost:<port>`,
  or `[::1]:<port>`. This blocks DNS-rebinding pages.
- **Origin.** A request that carries `Origin` must name the server's own
  origin. A cross-site `fetch` or form post is refused before any handler runs.
- **Content type.** Every non-`GET` request must be
  `Content-Type: application/json`. HTML forms cannot send that without CORS
  preflight, and the server answers no preflight.
- **Start token.** Every request, including the event streams, the client
  page, and its assets, must carry the start token. See *Authentication*.

The unix socket skips all of these checks. Its file mode is the access
control.

### Authentication

The checks above keep other web sites out, but any local program can connect
to a loopback port. So `projmux web` generates a start token when it starts:
32 bytes from `crypto/rand`, unpadded base64url. It exists only in the server
process and in the one line the server prints at start:

```text
projmux web: http://127.0.0.1:8787/?token=<token>
```

A TCP request is admitted when it carries the token in either form:

- the cookie `projmux_web_token_<port>`, which the server sets
  (`HttpOnly; SameSite=Strict; Path=/`, not `Secure`, since the listener is
  plain http on loopback). The port is in the name because browsers share
  cookies across ports on one host.
- `Authorization: Bearer <token>`, for clients other than a browser.

A `GET` or `HEAD` with `?token=<token>` sets that cookie and answers `303` to
`/`, whatever the request's path and other query parameters were, so the token
leaves the address bar and the browser history. The target is fixed so that a
request path can never make it an off-site redirect. The client then uses only
relative, same-origin `fetch` and `EventSource` URLs, which send the cookie on
their own.

A missing or wrong token, including a wrong `?token=`, is `401 unauthorized`
with the usual error envelope. The comparison is constant-time. The token is
never logged: request logs (`-v`) record the path only, and a refusal never
echoes what was sent. A new start makes a new token, so a browser tab from an
earlier start has to be reopened from the new URL.

The unix socket asks for no token.

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
| `unauthorized` | 401 | a TCP request without the start token, or with a wrong one |
| `forbidden-origin` | 403 | the Host, Origin, or Content-Type check failed |
| `not-found` | 404 | no such uid, or the uid is not under the given parent |
| `name-conflict` | 409 | the name is already used in that scope |
| `invalid-name` | 400 | the name fails metadata validation |
| `not-live` | 409 | the target has no live runtime to act on |
| `unsupported` | 400 | the provider has no such surface (for example, a turn on Claude) |
| `create-in-progress` | 409 | a window create for the same Project is still running on this server; a repeat press is refused rather than making a second window |
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
| DELETE | `/api/v1/projects/{project}/windows/{window}` | `delete window --yes` | body `{confirm}`; `?dryRun=true` (`delete window --dry-run`) needs no confirm, deletes nothing, and returns the plan and `runningAgents` (see *Delete dry runs*) |
| GET | `/api/v1/projects/{project}/windows/{window}/panes` | `get panes` | `PaneList` |
| GET | `/api/v1/projects/{project}/windows/{window}/panes/{pane}` | `get pane` | |
| PATCH | `/api/v1/projects/{project}/windows/{window}/panes/{pane}` | `rename pane` | body `{name}` |
| DELETE | `/api/v1/projects/{project}/windows/{window}/panes/{pane}` | `delete pane --yes` | body `{confirm}`; `?dryRun=true` (`delete pane --dry-run`) as for a window |
| POST | `/api/v1/projects/{project}/windows/{window}/panes/{pane}/focus` | `internal focus` | moves the operator's attached client |
| GET | `/api/v1/projects/{project}/windows/{window}/agents` | `get agents` | includes Offline agents the Window owns, which are the resume candidates |
| POST | `/api/v1/projects/{project}/windows/{window}/panes` | `create pane` | body `{anchorPane, cwdFrom?, confirm}`: a plain shell split to the right of `anchorPane` |
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
| GET | `/api/v1/usage` | status bar usage HUD and popup | `{hud, rows, unsupported, lastSync, syncSource, error}` from the cache only; never collects. `hud` is what the bar draws under the Settings visibility, `rows` what its popup lists |
| GET | `/api/v1/system` | status bar CPU and MEM | |

### Delete dry runs

A delete with `?dryRun=true` answers:

```json
{ "uid": "win-…", "dryRun": true, "plan": "<the CLI dry-run text>",
  "runningAgents": [ { "uid": "agent-…", "name": "codex" } ] }
```

`runningAgents` is never absent and is `[]` when nothing runs. It lists the
Agents whose stored phase is `Running` (the phase the graph carries) and that
the delete would stop: for a Pane, the Agent whose `status.paneRef` is that
Pane or that owns it; for a Window, every Agent the Window owns. It comes from
the same Registry read the delete is checked against. A delete without
`dryRun` returns `{uid, plan}` as before.

The client closes a Pane or a Window this way: it asks for the dry run first.
When `runningAgents` is empty it sends the confirmed delete at once, so closing
stays one click. Otherwise it shows one confirmation naming those Agents and
deletes only when the operator accepts; a cancel deletes nothing and reports
nothing.

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
| `topic-error` | the error envelope, with the topic in `details.topic` | a read failed; the stream stays open |

The first frame of each topic is sent at once. After that, a frame is sent
only on change, except that the first good read after a `topic-error` is
always sent, so a client can clear that topic's error when a frame for the
topic arrives. A `: keepalive` comment follows 20 seconds of silence.

No frame is named `error`: an `EventSource` fires its own `error` event when
the connection drops, and a client listening for one would take a topic
failure for a lost connection.

## Web routes

These routes exist for the browser client. They show the terminal, not the
Registry, so they are kept out of the core surface. They live under
`/api/v1/web` and follow the same conventions and error format.

| method | path | what it is |
| --- | --- | --- |
| GET | `/api/v1/web/i18n` | `{locale, messages}`: the `web.*` catalog for the resolved locale |
| GET | `/api/v1/web/agents/{agent}/transcript` | `{surface, repository, transcript}`: how the client may write to the agent, `{web, root, rev}` for linking references (GitHub origins only, else null), and the provider transcript flattened to turns (`?limit=`, 1–1000) |
| GET | `/api/v1/web/transcripts/events` | one SSE stream for every transcript a page follows; see *Transcript events* |
| GET | `/api/v1/web/windows/{window}/layout` | pane geometry in cells (`?contents=1` adds each pane's screen) |
| GET | `/api/v1/web/windows/{window}/layout/events` | SSE `layout` frames, sent on change; `gone` when the window is no longer there |
| GET | `/api/v1/web/panes/{pane}/screen` | one `capture-pane -e` of the pane, parsed into styled runs |
| GET | `/api/v1/web/panes/{pane}/screen/events` | SSE `screen` frames, sent on change; `gone` when the pane is no longer there |
| GET | `/api/v1/web/launch` | what the launcher offers, as the terminal AI launch picker: `{providers:[{id, name, ready}], defaultMode, claudeModels, claudeEfforts}` for the enabled providers |
| GET | `/api/v1/web/settings` | the Settings a web page consumes: `{ai:{defaultMode, modes, providers, splitCwdFrom}, statusbar:{…parts, usageProviders}, locale}` |
| PATCH | `/api/v1/web/settings` | `{key, value}`: changes one of them through the function the terminal Settings uses. Keys: `ai.defaultMode`, `ai.provider.<id>`, `ai.splitCwdFrom`, `statusbar.<notifications\|usage\|project\|working-directory\|git\|resources\|clock>`, `statusbar.usage.<provider>[.<window>]`, `locale`. A status bar change regenerates the tmux config and reloads the app server through its own logical socket, never an inherited `TMUX`; a save that lands but does not reload answers `refused` |
| GET | `/api/v1/web/statusbar` | which status bar parts Settings turned on: `{notifications, usage, project, workingDirectory, git, resources, clock}`, read with the functions the TUI renders from |
| GET | `/api/v1/web/panes/{pane}/git` | `{cwd, repo, branch, dirty, staged, ahead, behind}` for the directory the Registry records for the pane |
| GET | `/api/v1/web/windows/{window}/resume-candidates` | `{items}`: the window's agents with no live pane, each with its first and last transcript line |
| POST | `/api/v1/web/uploads` | an image body sent as `image/png`, `image/jpeg`, `image/webp`, or `image/gif` (at most 10 MiB, format checked from the bytes); kept as `<sha256>.<ext>` with mode 0600 under `<state>/web-uploads` for 7 days and answered with `{path, type, bytes}`. The composer puts the path where the image was pasted. This route alone takes a non-JSON body; the image types need a preflight as JSON does |
| GET | `/api/v1/web/uploads/{name}` | a stored image by its file name, for showing it in the conversation |
| POST | `/api/v1/web/agents/{agent}/question` | `{toolId, answers:[{picks, other}]}`: answers the Claude agent's pending AskUserQuestion; see *The question exception* |
| POST | `/api/v1/web/projects/{project}/windows/{window}/agents/preview` | `{argv}`: the exact command a create-agent request with the same body would run; runs nothing |

### Transcript events

`GET /api/v1/web/transcripts/events?stream=<key>:<agent>:<offset>&stream=…`
follows several agents' transcripts on one connection, so a page's
connection count does not grow with its agent slots (a browser allows six per
host over HTTP/1.1). Each `stream` value names a client-chosen key (1–16
letters, digits, `-`, `_`), the agent, and where to start: a byte offset (the
`offset` a transcript read returned, so nothing written in between is lost),
`start`, or `end`. At most 32 streams; a malformed or repeated key is
`invalid-request`.

| event | payload | sent when |
| --- | --- | --- |
| `ready` | `{streams: {key: offset}}` | once, after every transcript was opened |
| `turn` | `{stream, agent, turn}` | one per appended entry |
| `transcript-error` | `{stream, agent, error}` | a transcript could not be opened (it is left out of this connection) or a read failed (reported once per failing run; it is still followed) |
| `transcript-recovered` | `{stream, agent}` | a read succeeded after a failed one |

The `ready` frame and the last `turn` frame of each read carry the id
`key:offset,key:offset`, every key's resume offset. A reconnecting
`EventSource` sends it back as `Last-Event-ID`, and each key it names resumes
from there. The id moves only at the end of a read, so a stream cut inside a
read sends that read again; a client counts the frames of it it already
handled and skips them.

A turn is `{role, text, at, kind, thinking, from, messageRef, via, tools,
task, report, images}`. A background task finishing is one `task` turn with
`task: {id, kind, name, status, exitCode, outputFile, toolUses, durationMs,
tokens}` and no text; a subagent's final report is a `report` turn whose
text is the report without its delivery frame; system reminders are
dropped.
`from` is set only for a peer coordination message, and `via` is
`projmux-web` for a message this client sent. A coordination frame whose
source is operator input (`{"kind":"operator","client":"web"}`) is a `user`
turn with `via` `projmux-web` and no `from`. A frame whose source and target
are the same Agent is a `user` turn at every frame `schemaVersion`. Labels
such as "thinking" or "clipped" are the client's to localize; the server sends
flags and the note tokens `empty` and `no-transcript`.

Web routes take a bare uid, because uids are global. Each route resolves the
uid in the Registry and refuses a target that is not on the app-owned tmux
server.

### The question exception

projmux keeps agent input off raw pane keys. The web client makes one
contained exception, chosen by the operator: answering a Claude Code
AskUserQuestion, for which Claude Code has no channel a third party can call.
**It is to be replaced by that channel when one exists.** Sending text as the
operator's own words stays out.

`internal/web/question` is the only code in the web server that sends keys
to a pane; `TestPaneKeysStayInTheQuestionPackage` fails if any other web file
does. The route:

- takes only option indexes and free text. The question's shape is read from
  the agent's transcript, and a `toolId` that is not the pending question is
  refused (`question-changed`);
- resolves the pane from this request's observation of the app server, and
  refuses unless the pane still carries the agent's `@projmux_pane_uid`;
- captures the pane first and refuses unless the widget and the question's
  first line are on screen (`question-not-on-screen`);
- presses the widget's own keys, as measured against a live session: the
  option number for a single-select question, numbers and `Tab` for
  multi-select, the "Type something" number then the text then `Enter` for
  free text, and `1` on the review screen only when that screen is showing
  (`question-review-not-shown` otherwise, with the answers left unsent);
- refuses free text on a multi-select question, whose keys were not measured.

Where the client cannot answer, the card and a waiting slot offer "Open in
terminal", which moves the attached tmux client to the pane.

## Client addresses

Every path below serves the client; anything else outside `/api/` and
`/assets/` is a 404.

| path | view |
| --- | --- |
| `/` | the overview |
| `/project/{project}` | a Project |
| `/project/{project}/window/{window}` | a Window with all its slots |
| `/project/{project}/window/{window}/agent/{agent}` | the Window, focused on an agent's slot |
| `/project/{project}/window/{window}/pane/{pane}` | the Window, focused on a shell's slot |
| `/a/{agent}` | a short link; the client replaces it with the agent's full address |

The screen is always one real tmux window, as in the terminal; focusing a
slot in another window switches to that window. An agent is addressed by its
Agent uid because `agent resume` reuses the Agent but may allocate a new Pane.
Names never go in an address. A `/pane/` address for a pane an agent holds,
and any query an older client added (such as `?with=`), is rewritten in place.

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
  - *Questions.* The web client answers Claude questions through the
    contained key exception above until an answer channel exists.

  Codex is already covered: the app-server takes a turn as a user turn. The
  design is written up separately and agreed before anything is built.
- **Source maps.** Whether the built client's source maps are committed next
  to it depends on their size.
- **CI for the built client.** `make web-check` rebuilds the client and fails
  if the result differs from the committed files. Which CI job runs it must be
  decided without renaming any required job.

## Out of scope

- Remote access, multiple users, and authentication beyond the start token.
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
