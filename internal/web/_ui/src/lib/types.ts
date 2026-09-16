// The subset of the server's shapes the client reads. The server sends the
// Registry's own objects (projmux.io/v1alpha1); only fields used here are
// typed.

export interface Meta {
  uid: string;
  name: string;
  labels?: Record<string, string>;
  ownerRef?: { kind: string; uid: string };
  createdAt?: string;
}

export type LiveStatus = "live" | "offline" | "missing-root" | "unknown";

export interface RuntimeRef {
  kind: string;
  id: string;
  target?: string;
  name?: string;
}

export interface Project {
  metadata: Meta;
  spec: { root?: string };
  status: { session?: { name?: string; live?: boolean } };
}

export interface Window {
  metadata: Meta;
  spec: { anchorPaneRef?: string };
  status: {
    runtimeID?: string;
    conditions?: { type: string; status: string; reason?: string; message?: string }[];
  };
}

export interface Pane {
  metadata: Meta;
  spec: { role?: string; cwd?: string };
  status: { activation?: { runtimeID?: string } };
}

export interface Agent {
  metadata: Meta;
  spec: { provider?: string; workspace?: { cwd?: string } };
  status: {
    phase?: string;
    reason?: string;
    paneRef?: string;
    interaction?: { kind?: string; observedAt?: string };
  };
}

export interface Graph {
  hostMode: string;
  unavailable?: { scope: string; reason: string }[];
  projects?: { project: Project; status: LiveStatus; runtime?: RuntimeRef }[];
  windows?: {
    window: Window;
    projectUID?: string;
    status: LiveStatus;
    live?: boolean;
    active?: boolean;
    runtime?: RuntimeRef;
  }[];
  panes?: {
    pane: Pane;
    agentUID?: string;
    windowUID?: string;
    projectUID?: string;
    status: LiveStatus;
    runtime?: RuntimeRef;
  }[];
  agents?: {
    agent: Agent;
    windowUID?: string;
    projectUID?: string;
    paneUID?: string;
    status: LiveStatus;
  }[];
}

export interface Notification {
  id: string;
  text?: string;
  severity?: string;
  source?: string;
  session?: string;
  window?: string;
  pane?: string;
  metadata?: Record<string, string>;
  created_at?: string;
  expires_at?: string;
}

export interface UsageCell {
  model: string;
  window: string;
  pct: number;
  used?: number;
  limit?: number;
  resetsAt?: string;
  resetInSeconds?: number;
  updatedAt?: string;
  stale: boolean;
  fallback?: boolean;
}

export interface Usage {
  hud: UsageCell[];
  rows: UsageCell[];
  unsupported?: { model: string; label: string; reason: string }[];
  lastSync?: string;
  syncSource?: string;
  error?: string;
}

/** Which status bar parts Settings turned on. */
export interface StatusbarParts {
  notifications: boolean;
  usage: boolean;
  project: boolean;
  workingDirectory: boolean;
  git: boolean;
  resources: boolean;
  clock: boolean;
}

export interface PaneGit {
  cwd: string;
  repo?: string;
  branch?: string;
  dirty?: boolean;
  staged?: number;
  ahead?: number;
  behind?: number;
}

export interface System {
  supported: boolean;
  cpuPercent: number | null;
  memoryPercent: number | null;
}

export interface ToolCall {
  id?: string;
  name: string;
  summary?: string;
  input?: string;
  result?: string;
  error?: boolean;
  clipped?: boolean;
}

export interface TaskNote {
  id?: string;
  toolUseID?: string;
  kind?: "agent" | "command" | "";
  name?: string;
  status?: string;
  exitCode?: number;
  summary?: string;
  outputFile?: string;
  toolUses?: number;
  durationMs?: number;
  tokens?: number;
}

export interface Turn {
  role: string;
  text: string;
  at?: string;
  kind?: string;
  via?: string;
  thinking?: boolean;
  messageRef?: string;
  from?: { agentUID: string; provider?: string };
  tools?: ToolCall[];
  model?: string;
  effort?: string;
  task?: TaskNote;
  report?: { from?: string };
  images?: number;
}

export interface Repository {
  web: string;
  root: string;
  rev: string;
}

export interface Surface {
  mode: "turn" | "message" | "none";
  canStop?: boolean;
  sourceRequired?: boolean;
  maxBytes?: number;
}

export interface TranscriptView {
  surface: Surface;
  repository?: Repository | null;
  transcript: {
    provider: string;
    turns: Turn[] | null;
    truncated: boolean;
    note?: string;
    offset: number;
  };
}

export interface LayoutPane {
  runtime: string;
  x: number;
  y: number;
  width: number;
  height: number;
  active: boolean;
  command?: string;
  lines?: Run[][];
}

export interface Run {
  t: string;
  f?: string;
  b?: string;
  bo?: boolean;
  d?: boolean;
  i?: boolean;
  u?: boolean;
  r?: boolean;
}

export interface Layout {
  window: string;
  width: number;
  height: number;
  panes: LayoutPane[];
}

export interface ResumeCandidate {
  uid: string;
  name: string;
  provider: string;
  phase: string;
  opening?: string;
  last?: string;
  at?: string;
  turns: number;
  note?: string;
}
