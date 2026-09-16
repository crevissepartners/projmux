// The URL is the selection: /project/{p}/window/{w}/pane/{id}, with
// `?with=a,b` naming the panes opened beside the focused one. Pushing a URL is
// how anything changes selection, so Back and Forward walk through them and
// any view can be linked.

export interface Selection {
  project: string | null;
  window: string | null;
  pane: string | null;
}

function parse(): { sel: Selection; extras: string[] } {
  const parts = location.pathname.split("/").filter(Boolean);
  const at = (head: string, i: number) => (parts[i] === head ? parts[i + 1] || null : null);
  const extras = (new URLSearchParams(location.search).get("with") || "")
    .split(",")
    .map((uid) => uid.trim())
    .filter(Boolean);
  return {
    sel: { project: at("project", 0), window: at("window", 2), pane: at("pane", 4) },
    extras,
  };
}

export const route = $state(parse());

window.addEventListener("popstate", () => Object.assign(route, parse()));

export function pathFor(sel: Partial<Selection>): string {
  if (!sel.project) return "/";
  let path = `/project/${sel.project}`;
  if (sel.window) path += `/window/${sel.window}`;
  if (sel.window && sel.pane) path += `/pane/${sel.pane}`;
  return path;
}

function push(path: string, extras: string[], replace = false): void {
  const query = extras.length ? `?with=${extras.join(",")}` : "";
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
