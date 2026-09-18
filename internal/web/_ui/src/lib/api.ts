// The one way the client talks to the server.
//
// Every write is JSON: the server refuses anything else, because a form or a
// text/plain post is what a cross-site page can send without a preflight. The
// one exception is an image upload, sent as the image's own type.

export class ApiError extends Error {
  readonly code: string;
  readonly status: number;
  readonly details: Record<string, unknown>;

  constructor(code: string, message: string, status: number, details: Record<string, unknown> = {}) {
    super(message);
    this.code = code;
    this.status = status;
    this.details = details;
  }
}

interface Envelope {
  error?: { code?: string; message?: string; status?: number; details?: Record<string, unknown> };
}

export async function request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const init: RequestInit = { method, headers: { accept: "application/json" }, signal };
  if (body instanceof Blob) {
    init.headers = { ...init.headers, "content-type": body.type };
    init.body = body;
  } else if (body !== undefined) {
    init.headers = { ...init.headers, "content-type": "application/json" };
    init.body = JSON.stringify(body);
  }
  let res: Response;
  try {
    res = await fetch(path, init);
  } catch (err) {
    throw new ApiError("network", err instanceof Error ? err.message : String(err), 0);
  }
  let data: unknown = null;
  try {
    data = await res.json();
  } catch {
    /* an empty or non-JSON body is judged by the status alone */
  }
  if (!res.ok) {
    const envelope = (data as Envelope | null)?.error;
    throw new ApiError(
      envelope?.code || "http",
      envelope?.message || `HTTP ${res.status}`,
      res.status,
      envelope?.details || {},
    );
  }
  return data as T;
}

export const get = <T>(path: string, signal?: AbortSignal) => request<T>("GET", path, undefined, signal);
export const post = <T>(path: string, body: unknown = {}) => request<T>("POST", path, body);
export const patch = <T>(path: string, body: unknown) => request<T>("PATCH", path, body);
export const del = <T>(path: string, body: unknown = {}) => request<T>("DELETE", path, body);

export interface StoredUpload {
  path: string;
  type: string;
  bytes: number;
}

/** The image types the server keeps, and its size cap. */
export const UPLOAD_TYPES = ["image/png", "image/jpeg", "image/webp", "image/gif"];
export const UPLOAD_LIMIT = 10 * 1024 * 1024;

export const upload = (image: Blob) => request<StoredUpload>("POST", "/api/v1/web/uploads", image);

const seg = encodeURIComponent;

export const paths = {
  windows: (project: string) => `/api/v1/projects/${seg(project)}/windows`,
  window: (project: string, window: string) => `/api/v1/projects/${seg(project)}/windows/${seg(window)}`,
  pane: (project: string, window: string, pane: string) =>
    `/api/v1/projects/${seg(project)}/windows/${seg(window)}/panes/${seg(pane)}`,
  windowAgents: (project: string, window: string) =>
    `/api/v1/projects/${seg(project)}/windows/${seg(window)}/agents`,
  agentPreview: (project: string, window: string) =>
    `/api/v1/web/projects/${seg(project)}/windows/${seg(window)}/agents/preview`,
  agent: (agent: string) => `/api/v1/agents/${seg(agent)}`,
  agentGraph: (project: string) => `/api/v1/projects/${seg(project)}/agent-graph`,
  peerMessages: (agent: string, peer: string) => `/api/v1/agents/${seg(agent)}/peers/${seg(peer)}/messages`,
  notificationAck: (id: string) => `/api/v1/notifications/${seg(id)}/ack`,
  transcript: (agent: string) => `/api/v1/web/agents/${seg(agent)}/transcript`,
  transcriptEvents: "/api/v1/web/transcripts/events",
  question: (agent: string) => `/api/v1/web/agents/${seg(agent)}/question`,
  layout: (window: string) => `/api/v1/web/windows/${seg(window)}/layout`,
  statusbar: "/api/v1/web/statusbar",
  launch: "/api/v1/web/launch",
  settings: "/api/v1/web/settings",
  windowPanes: (project: string, window: string) => `/api/v1/projects/${seg(project)}/windows/${seg(window)}/panes`,
  usage: "/api/v1/usage",
  paneGit: (pane: string) => `/api/v1/web/panes/${seg(pane)}/git`,
  resumeCandidates: (window: string) => `/api/v1/web/windows/${seg(window)}/resume-candidates`,
};
