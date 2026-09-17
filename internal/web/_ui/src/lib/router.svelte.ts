// The URL is the selection, and the screen is one real tmux window, as in the
// terminal: /project/{p}/window/{w}/agent/{a} for an agent's slot or
// .../pane/{pane} for a shell's. `/a/{agent}` is the short form a link uses; it
// is replaced by the full address once the agent is found. Names never go in
// the URL. Pushing a URL is how anything changes selection, so Back and
// Forward walk through them and any view can be linked.

export interface Selection {
  project: string | null;
  window: string | null;
  /** A slot ref: the agent uid for an agent's pane, the pane uid for a shell. */
  pane: string | null;
}

interface Route {
  sel: Selection;
  /** The agent a short `/a/{agent}` address names, until it is resolved. */
  short: string | null;
  /** The address carries a query from an older client, such as `?with=`. */
  legacy: boolean;
}

function parse(): Route {
  const parts = location.pathname.split("/").filter(Boolean);
  const at = (head: string, i: number) => (parts[i] === head ? parts[i + 1] || null : null);
  return {
    sel: { project: at("project", 0), window: at("window", 2), pane: at("agent", 4) || at("pane", 4) },
    short: parts.length === 2 ? at("a", 0) : null,
    legacy: location.search !== "",
  };
}

export const route = $state<Route>(parse());

window.addEventListener("popstate", () => Object.assign(route, parse()));

export function pathFor(sel: Partial<Selection>): string {
  if (!sel.project) return "/";
  let path = `/project/${sel.project}`;
  if (sel.window) path += `/window/${sel.window}`;
  if (sel.window && sel.pane) path += sel.pane.startsWith("agent-") ? `/agent/${sel.pane}` : `/pane/${sel.pane}`;
  return path;
}

/** The short address for an agent, for links that leave this page. */
export function shortPath(agent: string): string {
  return `/a/${agent}`;
}

function push(path: string, replace = false): void {
  if (path === location.pathname + location.search) return;
  history[replace ? "replaceState" : "pushState"]({}, "", path);
  Object.assign(route, parse());
}

/** Rewrite the current URL in place, for an old spelling of the same view. */
export function canonicalize(sel: Selection): void {
  push(pathFor(sel), true);
}

/** Change the selection. A slot in another window switches to that window. */
export function go(sel: Partial<Selection>): void {
  push(pathFor(sel));
}
