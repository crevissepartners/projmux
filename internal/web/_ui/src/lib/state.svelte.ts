// What the server says, kept current by the event stream.
//
// One stream carries the graph, the notify queue, cached usage and host load.
// The server sends each topic at once and then only when it changes, so the
// page never polls.

import { get, ApiError } from "./api";
import { buildTree, emptyTree, type Tree } from "./tree";
import type { Graph, Notification, System, Usage } from "./types";

export const live = $state({
  tree: emptyTree as Tree,
  notifications: [] as Notification[],
  usage: { hud: [], rows: [] } as Usage,
  system: { supported: false, cpuPercent: null, memoryPercent: null } as System,
  connected: false,
  /** The last failure of each topic, cleared by that topic's next frame. */
  errors: {} as Record<string, string>,
  /** One of `errors`, for a place that shows a single line. */
  error: "",
  updatedAt: "",
});

let source: EventSource | null = null;

function take<T>(event: MessageEvent, apply: (body: T) => void): void {
  try {
    apply(JSON.parse(event.data) as T);
  } catch {
    /* skip one malformed frame */
  }
}

function setError(topic: string, message: string): void {
  if (message) live.errors[topic] = message;
  else if (topic in live.errors) delete live.errors[topic];
  else return;
  live.error = Object.values(live.errors)[0] || "";
}

/** Read one topic frame; a frame for a topic clears that topic's error. */
function follow<T>(stream: EventSource, topic: string, apply: (body: T) => void): void {
  stream.addEventListener(topic, (event) =>
    take<T>(event, (body) => {
      apply(body);
      setError(topic, "");
    }),
  );
}

export function connect(): void {
  source?.close();
  const stream = new EventSource("/api/v1/events");
  source = stream;
  stream.addEventListener("open", () => {
    live.connected = true;
  });
  stream.addEventListener("error", (event) => {
    // Only the EventSource's own error is a lost connection; it reconnects by
    // itself, and the flag shows it is trying. A topic failure is the
    // `topic-error` frame, which leaves the stream open.
    if (event instanceof MessageEvent) return;
    live.connected = false;
  });
  follow<Graph>(stream, "graph", (graph) => {
    live.tree = buildTree(graph);
    live.updatedAt = new Date().toISOString();
  });
  follow<{ items: Notification[] }>(stream, "notifications", (body) => {
    live.notifications = body.items || [];
  });
  follow<Usage>(stream, "usage", (body) => (live.usage = body));
  follow<System>(stream, "system", (body) => (live.system = body));
  stream.addEventListener("topic-error", (event) =>
    take<{ error: { message: string; details?: { topic?: string } } }>(event, (body) => {
      setError(body.error.details?.topic || "events", body.error.message);
    }),
  );
}

/** Read the graph once, for a change the page made and wants to see at once. */
export async function refresh(): Promise<void> {
  try {
    live.tree = buildTree(await get<Graph>("/api/v1/graph"));
    live.updatedAt = new Date().toISOString();
    setError("graph", "");
  } catch (err) {
    setError("graph", err instanceof ApiError ? err.message : String(err));
  }
}
