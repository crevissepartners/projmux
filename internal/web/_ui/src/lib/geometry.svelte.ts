// The live arrangement of one window's panes, in tmux cells.
//
// Only geometry is read here, never pane contents: the whole window is one
// tmux call. The stream sends a frame only when the arrangement changed.

import { paths } from "./api";
import type { Layout } from "./types";

export interface Box {
  x: number;
  y: number;
  width: number;
  height: number;
}

export const geometry = $state({ window: "", tmuxWindow: "", panes: {} as Record<string, Box> });

let source: EventSource | null = null;
let following = "";

function take(layout: Layout, window: string) {
  if (following !== window) return;
  const panes: Record<string, Box> = {};
  for (const p of layout.panes || []) panes[p.runtime] = { x: p.x, y: p.y, width: p.width, height: p.height };
  geometry.window = window;
  geometry.tmuxWindow = layout.window;
  geometry.panes = panes;
}

export function followGeometry(window: string): void {
  if (following === window) return;
  stopGeometry();
  following = window;
  const stream = new EventSource(`${paths.layout(window)}/events`);
  source = stream;
  stream.addEventListener("layout", (event) => {
    try {
      take(JSON.parse((event as MessageEvent).data) as Layout, window);
    } catch {
      /* skip one malformed frame */
    }
  });
  stream.addEventListener("gone", () => {
    if (source === stream) stopGeometry();
  });
}

export function stopGeometry(): void {
  source?.close();
  source = null;
  following = "";
  geometry.window = "";
  geometry.tmuxWindow = "";
  geometry.panes = {};
}
