// Splitting a pane, the way the terminal launcher does it: a target is picked
// (or the configured default is used) and the new pane opens to the right of
// the focused one at once.

import { get, paths, post } from "./api";
import { route, go } from "./router.svelte";
import { live, refresh } from "./state.svelte";
import { fail } from "./toast.svelte";
import { locateSlot } from "./tree";
import type { Agent, Pane } from "./types";
import { ui } from "./ui.svelte";

export interface LaunchProvider {
  id: string;
  name: string;
  ready: boolean;
}

export interface LaunchOptions {
  providers: LaunchProvider[];
  defaultMode: string;
  claudeModels: string[];
  claudeEfforts: string[];
}

export const launcher = $state({
  options: null as LaunchOptions | null,
  /** The target a split is running for, while it runs. */
  running: "",
});

export async function loadLaunchOptions(): Promise<LaunchOptions | null> {
  try {
    launcher.options = await get<LaunchOptions>(paths.launch);
  } catch (err) {
    fail(err);
  }
  return launcher.options;
}

/** The pane a split hangs off: the focused slot's pane. */
export function launchAnchor(): { project: string; window: string; pane: string } | null {
  const { project, window, pane } = route.sel;
  if (!project || !window || !pane) return null;
  const found = locateSlot(live.tree, pane);
  if (!found || found.win.uid !== window) return null;
  return { project, window, pane: found.pane.uid };
}

/**
 * Split `target` ("shell" or a provider) to the right of the focused pane and
 * focus the new slot. Returns false when nothing was created.
 */
export async function launch(target: string, options: { model?: string; effort?: string } = {}): Promise<boolean> {
  const anchor = launchAnchor();
  if (!anchor || launcher.running) return false;
  launcher.running = target;
  try {
    let ref: string | undefined;
    if (target === "shell") {
      const body = await post<{ pane?: Pane }>(paths.windowPanes(anchor.project, anchor.window), {
        anchorPane: anchor.pane,
        confirm: true,
      });
      ref = body.pane?.metadata.uid;
    } else {
      const body = await post<{ agent?: Agent; pane?: Pane }>(paths.windowAgents(anchor.project, anchor.window), {
        provider: target,
        anchorPane: anchor.pane,
        ...(options.model ? { model: options.model } : {}),
        ...(options.effort ? { effort: options.effort } : {}),
        confirm: true,
      });
      ref = body.agent?.metadata.uid || body.pane?.metadata.uid;
    }
    await refresh();
    if (ref) go({ project: anchor.project, window: anchor.window, pane: ref });
    return true;
  } catch (err) {
    fail(err);
    return false;
  } finally {
    launcher.running = "";
  }
}

/**
 * The split button: the configured default target, as the terminal's direct
 * split action. "selective" opens the launcher and "resume" the resume picker.
 */
export async function launchDefault(): Promise<void> {
  const options = await loadLaunchOptions();
  const mode = options?.defaultMode || "selective";
  if (mode === "resume") ui.overlay = "resume";
  else if (mode === "shell" || options?.providers.some((p) => p.id === mode && p.ready)) await launch(mode);
  else ui.overlay = "launch";
}
