// The Project / Window / Pane tree the screen navigates, assembled from the
// server's resource graph.
//
// The graph is the core API's read model: Registry rows with a runtime
// overlay, owners already resolved. This is only the view over it the page
// needs -- grouping, counts, ordering -- so the server keeps one read shape
// and the page's arrangement stays the page's business.

import { providerText } from "./errors";
import type { Agent, Graph, LiveStatus } from "./types";

export interface AgentView {
  uid: string;
  name: string;
  provider: string;
  phase: string;
  reason: string;
  activity: string;
  activityAt: string;
  paneUID: string;
  windowUID: string;
  cwd: string;
}

/**
 * Every Agent the graph has, whether or not a pane holds it, with what a card
 * names it by: its `role` label, its live status, and its Project.
 */
export interface AgentRecord extends AgentView {
  role: string;
  status: LiveStatus;
  projectUID: string;
}

export interface PaneView {
  uid: string;
  name: string;
  /** False when metadata.name is the uid, which the CLI fills in for an unnamed pane. */
  named: boolean;
  role: string;
  cwd: string;
  /** The observed tmux pane id, or "" when the pane is not live. */
  runtimeId: string;
  status: LiveStatus;
  agent: AgentView | null;
  windowUID: string;
  projectUID: string;
}

export interface WindowView {
  uid: string;
  name: string;
  projectUID: string;
  runtimeId: string;
  live: boolean;
  unbound: boolean;
  unboundReason: string;
  panes: PaneView[];
  /** Agents bound to a live pane in this window. */
  agentCount: number;
  /** Agents this window owns whose pane is gone: the resume candidates. */
  detached: AgentView[];
}

export interface ProjectView {
  uid: string;
  name: string;
  root: string;
  sessionName: string;
  sessionLive: boolean;
  windows: WindowView[];
  agentCount: number;
}

export interface Tree {
  projects: ProjectView[];
  /** Every Agent, in the graph's order. */
  agents: AgentRecord[];
  hostMode: string;
  unavailable: string[];
}

export const emptyTree: Tree = { projects: [], agents: [], hostMode: "", unavailable: [] };

function agentView(agent: Agent, windowUID: string, paneUID: string): AgentView {
  return {
    uid: agent.metadata.uid,
    name: agent.metadata.name,
    provider: (agent.spec.provider || "").toLowerCase(),
    phase: agent.status.phase || "",
    reason: agent.status.reason || "",
    activity: agent.status.interaction?.kind || "",
    activityAt: agent.status.interaction?.observedAt || "",
    paneUID,
    windowUID,
    cwd: agent.spec.workspace?.cwd || "",
  };
}

const lower = (text: string) => text.toLowerCase();

export function buildTree(graph: Graph): Tree {
  const agentsByUID = new Map<string, AgentView>();
  const agentsByWindow = new Map<string, AgentView[]>();
  const agents: AgentRecord[] = [];
  for (const node of graph.agents || []) {
    const view = agentView(node.agent, node.windowUID || "", node.paneUID || node.agent.status.paneRef || "");
    agentsByUID.set(view.uid, view);
    agents.push({
      ...view,
      role: node.agent.metadata.labels?.role || "",
      status: node.status,
      projectUID: node.projectUID || "",
    });
    const list = agentsByWindow.get(view.windowUID) || [];
    list.push(view);
    agentsByWindow.set(view.windowUID, list);
  }

  const panesByWindow = new Map<string, PaneView[]>();
  for (const node of graph.panes || []) {
    const pane = node.pane;
    const agent = node.agentUID ? agentsByUID.get(node.agentUID) || null : null;
    // The observed id is the one to act on. A stored activation id is a hint
    // that can outlive its pane.
    const runtimeId = node.status === "live" ? node.runtime?.id || pane.status.activation?.runtimeID || "" : "";
    const view: PaneView = {
      uid: pane.metadata.uid,
      name: pane.metadata.name,
      named: !!pane.metadata.name && pane.metadata.name !== pane.metadata.uid,
      role: pane.spec.role || "",
      cwd: pane.spec.cwd || agent?.cwd || "",
      runtimeId,
      status: node.status,
      agent: agent && agent.paneUID === pane.metadata.uid ? agent : null,
      windowUID: node.windowUID || "",
      projectUID: node.projectUID || "",
    };
    const list = panesByWindow.get(view.windowUID) || [];
    list.push(view);
    panesByWindow.set(view.windowUID, list);
  }
  for (const list of panesByWindow.values()) {
    // Agent panes first, then named ones, then by runtime id.
    list.sort((a, b) => {
      if (!!a.agent !== !!b.agent) return a.agent ? -1 : 1;
      if (a.named !== b.named) return a.named ? -1 : 1;
      return a.runtimeId.localeCompare(b.runtimeId);
    });
  }

  const windowsByProject = new Map<string, WindowView[]>();
  for (const node of graph.windows || []) {
    const win = node.window;
    const unbound = (win.status.conditions || []).find(
      (c) => c.status === "True" && (c.type === "MissingRuntime" || c.type === "RuntimeUnbound"),
    );
    const panes = panesByWindow.get(win.metadata.uid) || [];
    const livePaneUIDs = new Set(panes.filter((p) => p.runtimeId).map((p) => p.uid));
    const owned = agentsByWindow.get(win.metadata.uid) || [];
    const bound = new Set(panes.filter((p) => p.agent && p.runtimeId).map((p) => p.agent!.uid));
    const view: WindowView = {
      uid: win.metadata.uid,
      name: win.metadata.name,
      projectUID: node.projectUID || win.metadata.ownerRef?.uid || "",
      runtimeId: node.live ? node.runtime?.id || win.status.runtimeID || "" : "",
      live: !!node.live,
      unbound: !!unbound,
      unboundReason: unbound ? unbound.message || unbound.reason || "" : "",
      panes,
      agentCount: bound.size,
      detached: owned.filter((a) => !bound.has(a.uid) && (!a.paneUID || !livePaneUIDs.has(a.paneUID))),
    };
    const list = windowsByProject.get(view.projectUID) || [];
    list.push(view);
    windowsByProject.set(view.projectUID, list);
  }
  for (const list of windowsByProject.values()) {
    // Live windows first, then by name.
    list.sort((a, b) => {
      if (a.unbound !== b.unbound) return a.unbound ? 1 : -1;
      return lower(a.name).localeCompare(lower(b.name));
    });
  }

  const projects: ProjectView[] = (graph.projects || []).map((node) => {
    const project = node.project;
    const windows = windowsByProject.get(project.metadata.uid) || [];
    return {
      uid: project.metadata.uid,
      name: project.metadata.name,
      root: project.spec.root || "",
      sessionName: project.status.session?.name || "",
      sessionLive: node.status === "live" || !!node.runtime,
      windows,
      agentCount: windows.reduce((sum, w) => sum + w.agentCount, 0),
    };
  });
  projects.sort((a, b) => {
    if (a.sessionLive !== b.sessionLive) return a.sessionLive ? -1 : 1;
    return lower(a.name).localeCompare(lower(b.name));
  });

  return {
    projects,
    agents,
    hostMode: graph.hostMode,
    unavailable: (graph.unavailable || []).map((u) => `${u.scope}: ${u.reason}`),
  };
}

export interface Located {
  project: ProjectView;
  win: WindowView;
  pane: PaneView;
}

export function findProject(tree: Tree, uid: string | null): ProjectView | null {
  return tree.projects.find((p) => p.uid === uid) || null;
}

export function findWindow(project: ProjectView | null, uid: string | null): WindowView | null {
  return project?.windows.find((w) => w.uid === uid) || null;
}

export function findPane(win: WindowView | null, uid: string | null): PaneView | null {
  return win?.panes.find((p) => p.uid === uid) || null;
}

/**
 * How the URL names a slot: an agent's slot by its agent, a shell by its
 * pane. Resuming an agent can give it a new pane, and a link or a split
 * should still find it; the agent uid is what stays.
 */
export function slotRef(pane: PaneView): string {
  return pane.agent?.uid || pane.uid;
}

/** The pane a slot ref names, by agent uid or pane uid. */
export function findSlot(win: WindowView | null, ref: string | null): PaneView | null {
  if (!win || !ref) return null;
  return win.panes.find((p) => p.uid === ref || p.agent?.uid === ref) || null;
}

export function locateSlot(tree: Tree, ref: string): Located | null {
  for (const project of tree.projects) {
    for (const win of project.windows) {
      const pane = findSlot(win, ref);
      if (pane) return { project, win, pane };
    }
  }
  return null;
}

export function locatePane(tree: Tree, uid: string): Located | null {
  for (const project of tree.projects) {
    for (const win of project.windows) {
      for (const pane of win.panes) {
        if (pane.uid === uid) return { project, win, pane };
      }
    }
  }
  return null;
}

export function paneByRuntime(tree: Tree, runtimeId: string | undefined): Located | null {
  if (!runtimeId) return null;
  for (const project of tree.projects) {
    for (const win of project.windows) {
      for (const pane of win.panes) {
        if (pane.runtimeId === runtimeId) return { project, win, pane };
      }
    }
  }
  return null;
}

/** Every running agent pane, optionally in one project. */
export function livePanes(tree: Tree, onlyProject?: ProjectView | null): Located[] {
  const rows: Located[] = [];
  for (const project of tree.projects) {
    if (onlyProject && project.uid !== onlyProject.uid) continue;
    for (const win of project.windows) {
      for (const pane of win.panes) {
        if (pane.agent?.phase === "Running" && pane.runtimeId) rows.push({ project, win, pane });
      }
    }
  }
  return rows;
}

/**
 * A pane's display name. An agent's pane is labelled by its agent, as projmux
 * does everywhere: `create agent` names the pane `<agent>-pane`.
 */
export function paneLabel(pane: PaneView): { name: string; dim: boolean } {
  const agent = pane.agent;
  // An agent nobody named carries its uid as its name, which is an internal
  // identifier, not a label; the provider says more.
  if (agent?.name && agent.name !== agent.uid) return { name: agent.name, dim: false };
  if (agent) return { name: providerText(agent.provider), dim: true };
  if (pane.named) return { name: pane.name, dim: false };
  return { name: providerText("shell"), dim: true };
}

/**
 * An Agent's display name by uid, from its record when the graph has one. An
 * Agent nobody named reads as its provider; one the graph does not have at
 * all can only be shown by its uid.
 */
export function agentTitle(record: AgentRecord | null | undefined, uid: string): { name: string; dim: boolean } {
  if (!record) return { name: uid, dim: true };
  if (record.name && record.name !== record.uid) return { name: record.name, dim: false };
  return { name: providerText(record.provider) || uid, dim: true };
}

/**
 * Whether a card may say an Agent is online. `missing-root` is offline too;
 * `unknown`, and an Agent the graph does not have, say only that nobody can
 * tell, never that it is offline.
 */
export function onlineOf(record: AgentRecord | null | undefined): "online" | "offline" | "unknown" {
  switch (record?.status) {
    case "live":
      return "online";
    case "offline":
    case "missing-root":
      return "offline";
    default:
      return "unknown";
  }
}
