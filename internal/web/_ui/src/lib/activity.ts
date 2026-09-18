// What an agent is doing, as its provider hooks report it. `idle` and an
// unknown state are the resting state and show nothing.

import type { AgentView, PaneView, Located } from "./tree";

export type Tone = "busy" | "wait" | "alert" | "done";

const tones: Record<string, Tone> = {
  in_progress: "busy",
  input_required: "wait",
  approval_required: "alert",
  response_complete: "done",
};

export function activityOf(pane: PaneView | null | undefined): { kind: string; tone: Tone } | null {
  return agentActivityOf(pane?.agent);
}

// The same reading for an Agent that is drawn without its pane, as an Agent
// card is.
export function agentActivityOf(agent: AgentView | null | undefined): { kind: string; tone: Tone } | null {
  if (!agent || agent.phase !== "Running") return null;
  const tone = tones[agent.activity];
  return tone ? { kind: agent.activity, tone } : null;
}

export const needsYou = (tone: Tone | undefined) => tone === "wait" || tone === "alert";

// Waiting on you first, then working, then done, then the rest.
const order: Record<Tone, number> = { alert: 0, wait: 1, busy: 2, done: 3 };

export function toneRank(pane: PaneView): number {
  const tone = activityOf(pane)?.tone;
  return tone ? order[tone] : 9;
}

export function byAttention(a: Located, b: Located): number {
  const diff = toneRank(a.pane) - toneRank(b.pane);
  if (diff) return diff;
  return (b.pane.agent?.activityAt || "").localeCompare(a.pane.agent?.activityAt || "");
}
