// Actions the page takes. Each one refreshes the graph afterwards so the
// result is on screen before the next event arrives.

import { ApiError, del, patch, paths, post } from "./api";
import { t } from "./i18n.svelte";
import { go } from "./router.svelte";
import { refresh } from "./state.svelte";
import { fail, toast } from "./toast.svelte";
import type { Agent, Pane, Window } from "./types";

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
export async function createWindow(project: string): Promise<void> {
  try {
    const body = await post<CreatedWindow>(paths.windows(project), {
      agent: { provider: "claude" },
      focus: true,
      confirm: true,
    });
    if (body.agentError) toast(body.agentError.message, "err");
    await refresh();
    const window = body.window?.metadata.uid;
    if (window) go({ project, window, pane: body.pane?.metadata.uid ?? null });
  } catch (err) {
    fail(err);
  }
}

export async function closeWindow(project: string, window: string): Promise<boolean> {
  try {
    await del(paths.window(project, window), { confirm: true });
    await refresh();
    return true;
  } catch (err) {
    fail(err);
    return false;
  }
}

export async function closePane(project: string, window: string, pane: string): Promise<boolean> {
  try {
    await del(paths.pane(project, window, pane), { confirm: true });
    await refresh();
    return true;
  } catch (err) {
    fail(err);
    return false;
  }
}

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
