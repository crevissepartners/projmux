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

export function connect(): void {
  source?.close();
  const stream = new EventSource("/api/v1/events");
  source = stream;
  stream.addEventListener("open", () => {
    live.connected = true;
  });
  stream.addEventListener("error", () => {
    // EventSource reconnects by itself; the flag shows it is trying.
    live.connected = false;
  });
  stream.addEventListener("graph", (event) =>
    take<Graph>(event, (graph) => {
      live.tree = buildTree(graph);
      live.updatedAt = new Date().toISOString();
      live.error = "";
    }),
  );
  stream.addEventListener("notifications", (event) =>
    take<{ items: Notification[] }>(event, (body) => {
      live.notifications = body.items || [];
    }),
  );
  stream.addEventListener("usage", (event) => take<Usage>(event, (body) => (live.usage = body)));
  stream.addEventListener("system", (event) => take<System>(event, (body) => (live.system = body)));
  stream.addEventListener("error", (event) => {
    if (!(event instanceof MessageEvent)) return;
    take<{ error: { message: string } }>(event, (body) => {
      live.error = body.error.message;
    });
  });
}

/** Read the graph once, for a change the page made and wants to see at once. */
export async function refresh(): Promise<void> {
  try {
    live.tree = buildTree(await get<Graph>("/api/v1/graph"));
    live.updatedAt = new Date().toISOString();
  } catch (err) {
    live.error = err instanceof ApiError ? err.message : String(err);
  }
}
