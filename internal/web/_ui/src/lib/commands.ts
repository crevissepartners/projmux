// Actions the page takes. Each one refreshes the graph afterwards so the
// result is on screen before the next event arrives.

import { ApiError, del, patch, paths, post } from "./api";
import { t } from "./i18n.svelte";
import { go } from "./router.svelte";
import { refresh } from "./state.svelte";
import { fail, toast } from "./toast.svelte";
import type { Agent, Pane, Window } from "./types";
import { ui } from "./ui.svelte";

interface CreatedWindow {
  window?: Window;
  agent?: Agent;
  pane?: Pane;
  agentError?: { message: string };
  focusError?: { message: string };
}

/**
 * A new Window with a Claude already in it, focused, which is what the
 * new-window key means. The Window exists even when the agent is refused, so
 * that is reported and the page still goes there.
 */
// A create takes seconds. Until it answers, pressing Alt-N again or the +
// button does nothing here, and the server refuses a second create for the
// same Project from another tab.
export async function createWindow(project: string): Promise<void> {
  if (ui.creatingWindow) return;
  ui.creatingWindow = project;
  try {
    const body = await post<CreatedWindow>(paths.windows(project), {
      agent: { provider: "claude" },
      focus: true,
      confirm: true,
    });
    if (body.agentError) toast(body.agentError.message, "err");
    await refresh();
    const window = body.window?.metadata.uid;
    if (window) go({ project, window, pane: body.agent?.metadata.uid ?? body.pane?.metadata.uid ?? null });
  } catch (err) {
    fail(err);
  } finally {
    ui.creatingWindow = "";
  }
}

interface DeletePlan {
  runningAgents?: { uid: string; name: string }[];
}

/**
 * Closing asks the server for the plan first. When the plan would stop a
 * Running Agent, one confirmation names those Agents and only an accept
 * deletes; otherwise the delete follows at once, still one click. A cancel is
 * not a failure: nothing is deleted and nothing is reported.
 */
async function closeConfirmed(path: string, question: string): Promise<boolean> {
  try {
    const plan = await del<DeletePlan>(`${path}?dryRun=true`);
    const running = plan.runningAgents ?? [];
    if (running.length > 0) {
      const names = running.map((agent) => agent.name || agent.uid).join(", ");
      if (!window.confirm(t(question, { agents: names }))) return false;
    }
    await del(path, { confirm: true });
    await refresh();
    return true;
  } catch (err) {
    fail(err);
    return false;
  }
}

export const closeWindow = (project: string, window: string): Promise<boolean> =>
  closeConfirmed(paths.window(project, window), "web.windows.close_running");

export const closePane = (project: string, window: string, pane: string): Promise<boolean> =>
  closeConfirmed(paths.pane(project, window, pane), "web.slot.close_running");

async function renamed(path: string, name: string): Promise<void> {
  try {
    await patch(path, { name });
    toast(t("web.rename.done", { name }));
    await refresh();
  } catch (err) {
    fail(err);
  }
}

export const renameWindow = (project: string, window: string, name: string) =>
  renamed(paths.window(project, window), name);

/** An agent's pane is labelled by its agent, so renaming it renames the agent. */
export const renamePane = (project: string, window: string, pane: string, agent: string | null, name: string) =>
  renamed(agent ? paths.agent(agent) : paths.pane(project, window, pane), name);

export async function focusPane(project: string, window: string, pane: string): Promise<void> {
  try {
    await post(`${paths.pane(project, window, pane)}/focus`);
  } catch (err) {
    fail(err);
  }
}

export function isRefusal(err: unknown, code: string): boolean {
  return err instanceof ApiError && err.code === code;
}
