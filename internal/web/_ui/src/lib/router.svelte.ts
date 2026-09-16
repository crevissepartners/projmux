// The URL is the selection: /project/{p}/window/{w}/agent/{a} for an agent's
// slot or .../pane/{pane} for a shell's, with `?with=a:{uid},p:{uid}` naming
// the slots opened beside the focused one by kind. `/a/{agent}` is the short
// form a link uses; it is replaced by the full address once the agent is
// found. Names never go in the URL. Pushing a URL is how anything changes
// selection, so Back and Forward walk through them and any view can be linked.

export interface Selection {
  project: string | null;
  window: string | null;
  /** A slot ref: the agent uid for an agent's pane, the pane uid for a shell. */
  pane: string | null;
}

interface Route {
  sel: Selection;
  extras: string[];
  /** The agent a short `/a/{agent}` address names, until it is resolved. */
  short: string | null;
  /** The query named a slot without its kind, as addresses once did. */
  legacy: boolean;
}

const KIND = /^([ap]):/;

function parse(): Route {
  const parts = location.pathname.split("/").filter(Boolean);
  const at = (head: string, i: number) => (parts[i] === head ? parts[i + 1] || null : null);
  const entries = (new URLSearchParams(location.search).get("with") || "")
    .split(",")
    .map((entry) => entry.trim())
    .filter(Boolean);
  return {
    sel: { project: at("project", 0), window: at("window", 2), pane: at("agent", 4) || at("pane", 4) },
    extras: entries.map((entry) => entry.replace(KIND, "")),
    short: parts.length === 2 ? at("a", 0) : null,
    legacy: entries.some((entry) => !KIND.test(entry)),
  };
}

/** The short address for an agent, for links that leave this page. */
export function shortPath(agent: string): string {
  return `/a/${agent}`;
}

const withKind = (ref: string) => `${ref.startsWith("agent-") ? "a" : "p"}:${ref}`;

export const route = $state<Route>(parse());

window.addEventListener("popstate", () => Object.assign(route, parse()));

export function pathFor(sel: Partial<Selection>): string {
  if (!sel.project) return "/";
  let path = `/project/${sel.project}`;
  if (sel.window) path += `/window/${sel.window}`;
  if (sel.window && sel.pane) path += sel.pane.startsWith("agent-") ? `/agent/${sel.pane}` : `/pane/${sel.pane}`;
  return path;
}

/** Rewrite the current URL in place, for an old spelling of the same view. */
export function canonicalize(sel: Selection, extras: string[]): void {
  push(pathFor(sel), extras, true);
}

function push(path: string, extras: string[], replace = false): void {
  const query = extras.length ? `?with=${extras.map(withKind).join(",")}` : "";
  const url = path + query;
  if (url === location.pathname + location.search) return;
  history[replace ? "replaceState" : "pushState"]({}, "", url);
  Object.assign(route, parse());
}

/** Change the selection. Opened-beside panes are dropped unless kept. */
export function go(sel: Partial<Selection>, keepExtras = false): void {
  push(pathFor(sel), keepExtras ? route.extras : []);
}

export function setExtras(extras: string[]): void {
  push(location.pathname, extras);
}

/**
 * Focus a pane already on screen. The focused pane moves into the path and the
 * one it displaced joins the others, so nothing on screen disappears.
 */
export function focusOnScreen(sel: Selection): void {
  if (!sel.pane || route.sel.pane === sel.pane) return;
  const others = route.extras.filter((uid) => uid !== sel.pane);
  if (route.sel.pane) others.unshift(route.sel.pane);
  push(pathFor(sel), others);
}

/** Close one on-screen pane; closing the focused one promotes the next. */
export function closeOnScreen(uid: string, locate: (uid: string) => Selection | null): void {
  if (route.sel.pane !== uid) {
    setExtras(route.extras.filter((other) => other !== uid));
    return;
  }
  const others = [...route.extras];
  const next = others.shift();
  if (!next) {
    push(pathFor({ project: route.sel.project, window: route.sel.window }), []);
    return;
  }
  const found = locate(next);
  push(found ? pathFor(found) : "/", others);
}
