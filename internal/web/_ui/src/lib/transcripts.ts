// Every transcript the page follows, over one server-sent-events connection.
//
// A browser allows six connections per host over HTTP/1.1. A stream per agent
// slot filled them at four slots, and every request after that waited
// forever. So the page opens one stream for all of them: each subscriber gets
// a key, and the stream is reopened (once, after a short pause) when the set
// of keys changes.
//
// Resuming loses nothing. The stream's frame ids record where each key got
// to, a reopen starts every key there, and the browser sends the last id back
// on its own reconnects. The id only moves at the end of one read, so a
// stream cut inside a read sends that read again; the frames of it this page
// already handed out are counted and skipped.

import { paths } from "./api";
import type { Turn } from "./types";

export type StreamState = "live" | "warn";

export interface TranscriptHandlers {
  turn: (turn: Turn) => void;
  state: (state: StreamState) => void;
}

interface Subscription {
  key: string;
  agent: string;
  /** Where the subscriber's own read ended; -1 for the end of the file. */
  from: number;
  handlers: TranscriptHandlers;
}

interface Frame {
  stream: string;
  agent: string;
  turn?: Turn;
}

const REOPEN_MS = 50;
const RETRY_MS = 5000;

const subs = new Map<string, Subscription>();
let counter = 0;
let source: EventSource | null = null;
let timer: ReturnType<typeof setTimeout> | undefined;

// Resume bookkeeping, per key.
let lastId = "";
const committed = new Map<string, number>(); // offset in the last frame id
const seen = new Map<string, number>(); // frames handed out past that offset
const skip = new Map<string, number>(); // frames this connection resends
const pos = new Map<string, number>(); // frames this connection sent past that offset

function parseId(id: string): void {
  for (const pair of id.split(",")) {
    const [key, raw] = pair.split(":");
    const offset = Number(raw);
    if (key && raw && Number.isInteger(offset) && offset >= 0) committed.set(key, offset);
  }
}

function schedule(delay = REOPEN_MS): void {
  clearTimeout(timer);
  timer = setTimeout(reopen, delay);
}

function each(fn: (sub: Subscription) => void): void {
  for (const sub of subs.values()) fn(sub);
}

function frame(event: Event): Frame | null {
  try {
    const body = JSON.parse((event as MessageEvent).data) as Frame;
    return body && typeof body.stream === "string" ? body : null;
  } catch {
    return null; // skip one malformed frame
  }
}

function reopen(): void {
  timer = undefined;
  source?.close();
  source = null;
  for (const map of [committed, seen, skip, pos]) {
    for (const key of [...map.keys()]) if (!subs.has(key)) map.delete(key);
  }
  if (!subs.size) {
    lastId = "";
    return;
  }
  const query = new URLSearchParams();
  each((sub) => {
    const offset = committed.get(sub.key) ?? sub.from;
    query.append("stream", `${sub.key}:${sub.agent}:${offset < 0 ? "end" : offset}`);
  });
  lastId = "";
  const stream = new EventSource(`${paths.transcriptEvents}?${query}`);
  source = stream;

  stream.addEventListener("open", () => {
    // The server resumes every key at its committed offset, so the frames
    // handed out past it come again.
    for (const key of subs.keys()) {
      skip.set(key, seen.get(key) || 0);
      pos.set(key, 0);
    }
    each((sub) => sub.handlers.state("live"));
  });
  stream.addEventListener("error", (event) => {
    if (event instanceof MessageEvent || source !== stream) return;
    each((sub) => sub.handlers.state("warn"));
    // A refused stream is not retried by the browser.
    if (stream.readyState === EventSource.CLOSED) schedule(RETRY_MS);
  });
  stream.addEventListener("ready", (event) => {
    lastId = (event as MessageEvent).lastEventId;
    parseId(lastId);
  });
  stream.addEventListener("turn", (event) => {
    const body = frame(event);
    if (!body || !body.turn) return;
    const key = body.stream;
    const at = (pos.get(key) || 0) + 1;
    pos.set(key, at);
    if (at > (skip.get(key) || 0)) {
      try {
        subs.get(key)?.handlers.turn(body.turn);
      } catch {
        /* one subscriber's failure does not stop the others */
      }
    }
    seen.set(key, Math.max(seen.get(key) || 0, at));
    const id = (event as MessageEvent).lastEventId;
    if (id !== lastId) {
      // The last frame of a read: everything of this key so far is committed.
      lastId = id;
      parseId(id);
      seen.set(key, 0);
      skip.set(key, 0);
      pos.set(key, 0);
    }
  });
  stream.addEventListener("transcript-error", (event) => {
    const body = frame(event);
    if (body) subs.get(body.stream)?.handlers.state("warn");
  });
  stream.addEventListener("transcript-recovered", (event) => {
    const body = frame(event);
    if (body) subs.get(body.stream)?.handlers.state("live");
  });
}

/**
 * Follow an agent's transcript from `from`, the offset its transcript read
 * ended at. Returns the call that stops following.
 */
export function followTranscript(agent: string, from: number, handlers: TranscriptHandlers): () => void {
  const key = (++counter).toString(36);
  subs.set(key, { key, agent, from, handlers });
  schedule();
  return () => {
    if (!subs.delete(key)) return;
    schedule();
  };
}
