// Where the Agent graph puts each card, as a pure function of the graph.
//
// The same agents and edges always give the same coordinates: the input is
// copied and sorted before anything is placed, nothing is random, nothing
// reads the clock, and no Map is iterated except over keys that were sorted
// first. Two screenshots of the same graph can be compared, and a card does
// not jump when the page reads the graph again.
//
// Agents are laid out by hierarchy, left to right:
//
//  - Column. An Agent's role rank comes from its `role` label: no label or
//    `portfolio` is 0, `epic-owner` 1, `epic-worker` 2, any other label 3. An
//    Agent the live graph does not have at all (role `null`) is 3 as well: its
//    label cannot be read, so it is not claimed to be a top-level Agent. An
//    Agent whose creator is in the response (a `created` edge from it) sits at
//    max(its rank, its creator's column + 1). If an Agent has more than one
//    creator edge (the server sends at most one), the creator with the
//    smallest uid counts. An Agent on a creator cycle, which the server should
//    never send, falls back to its rank. Conversation edges never move a
//    card. Columns nobody uses are dropped, across the whole graph, so the
//    leftmost used column is always the first one, and a column has the same
//    x in every area.
//
//  - Row, within one area and one column, columns taken left to right. An
//    Agent whose creator already sits in this area (an earlier column) is
//    placed first, in the order (creator's row, display name, uid), each on
//    its creator's row when that row is still free and on the next free row
//    below otherwise, so children line up beside their creator. Every other
//    Agent follows on the next free rows: first those that created an Agent
//    in the graph, then the rest, each part in (display name, uid) order, so
//    a creator is not pushed below a run of Agents that created nobody.
//
//  - Areas. This Project's area is on top. Each other Project with an Agent in
//    the graph is a labelled box of its own below it, in projectUID order,
//    laid out by the same rules. Every area is as wide as all the used
//    columns, so columns line up from box to box.
//
// Placement is not overlap-free for every line; the page scrolls instead.

export const CARD_W = 208;
export const CARD_H = 76;
/** Between two columns: wide enough to show an arrow and its head. */
const GAP_X = 64;
const GAP_Y = 20;
const STEP_X = CARD_W + GAP_X;
const STEP_Y = CARD_H + GAP_Y;
/** Inside an area, around its cards. */
const PAD = 20;
/** An area's label row, above its cards. */
const LABEL = 24;
/** Between two areas. */
const AREA_GAP = 40;
/** Around everything, so a curve or a shadow is never cut off. */
const MARGIN = 12;

export interface LayoutInput {
  /** The Project the graph is about. */
  project: string;
  agents: readonly {
    uid: string;
    projectUID: string;
    /** The `role` label; "" when it has none, null when the Agent is not in the live graph. */
    role: string | null;
    /** What the card is titled; orders the cards within a column. */
    name: string;
  }[];
  edges: readonly { kind: string; a: string; b: string }[];
}

export interface CardBox {
  uid: string;
  projectUID: string;
  /** The hierarchy column, after unused columns are dropped. */
  column: number;
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface AreaBox {
  projectUID: string;
  /** The Project the graph is about, rather than an outside one. */
  home: boolean;
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface EdgePath {
  kind: "conversation" | "created";
  a: string;
  b: string;
  /** An SVG path. A created one runs from the creator `a` to the child `b`. */
  d: string;
}

export interface AgentGraphLayout {
  width: number;
  height: number;
  cards: CardBox[];
  areas: AreaBox[];
  edges: EdgePath[];
}

const byString = (x: string, y: string) => (x < y ? -1 : x > y ? 1 : 0);
const round = (v: number) => Math.round(v * 10) / 10;

/** The rank a `role` label gives; see the rules above. */
export function roleRank(role: string | null): number {
  if (role === null) return 3;
  switch (role) {
    case "":
    case "portfolio":
      return 0;
    case "epic-owner":
      return 1;
    case "epic-worker":
      return 2;
    default:
      return 3;
  }
}

export function layoutAgentGraph(input: LayoutInput): AgentGraphLayout {
  const agents = [...input.agents].sort((x, y) => byString(x.uid, y.uid));
  const byUID = new Map(agents.map((a) => [a.uid, a]));

  // Each Agent's creator: the smallest-uid creator in the response.
  const creator = new Map<string, string>();
  const createdEdges = input.edges
    .filter((e) => e.kind === "created" && e.a !== e.b && byUID.has(e.a) && byUID.has(e.b))
    .sort((x, y) => byString(x.a, y.a) || byString(x.b, y.b));
  for (const e of createdEdges) if (!creator.has(e.b)) creator.set(e.b, e.a);

  // With one creator each, following creators from an Agent either ends or
  // comes back round; an Agent it comes back to is on a cycle.
  const onCycle = (uid: string): boolean => {
    let at = creator.get(uid);
    for (let i = 0; at !== undefined && i < agents.length; i++) {
      if (at === uid) return true;
      at = creator.get(at);
    }
    return false;
  };
  const cyclic = new Set(agents.filter((a) => onCycle(a.uid)).map((a) => a.uid));
  const creators = new Set([...creator.values()]);

  // Memoised: a chain from an Agent off every cycle ends at an Agent with no
  // creator or at one on a cycle, both of which answer without recursing.
  const depth = new Map<string, number>();
  const columnOf = (uid: string): number => {
    const known = depth.get(uid);
    if (known !== undefined) return known;
    const rank = roleRank(byUID.get(uid)!.role);
    const parent = creator.get(uid);
    const value = parent === undefined || cyclic.has(uid) ? rank : Math.max(rank, columnOf(parent) + 1);
    depth.set(uid, value);
    return value;
  };
  const used = [...new Set(agents.map((a) => columnOf(a.uid)))].sort((x, y) => x - y);
  const columnIndex = new Map(used.map((c, i) => [c, i]));
  const column = (uid: string) => columnIndex.get(columnOf(uid))!;
  const columns = Math.max(1, used.length);
  const areaW = PAD * 2 + columns * STEP_X - GAP_X;

  // One area: rows per column, then its box.
  const cards: CardBox[] = [];
  const areas: AreaBox[] = [];
  let top = MARGIN;
  const placeArea = (projectUID: string, home: boolean, members: typeof agents) => {
    const row = new Map<string, number>();
    let rows = 0;
    for (let c = 0; c < columns; c++) {
      const here = members.filter((a) => column(a.uid) === c);
      const parentRow = (uid: string) => {
        const parent = cyclic.has(uid) ? undefined : creator.get(uid);
        return parent === undefined ? undefined : row.get(parent);
      };
      const byName = (x: (typeof agents)[number], y: (typeof agents)[number]) =>
        byString(x.name, y.name) || byString(x.uid, y.uid);
      const children = here
        .filter((a) => parentRow(a.uid) !== undefined)
        .sort((x, y) => parentRow(x.uid)! - parentRow(y.uid)! || byName(x, y));
      const creatorFirst = (x: (typeof agents)[number], y: (typeof agents)[number]) =>
        Number(!creators.has(x.uid)) - Number(!creators.has(y.uid)) || byName(x, y);
      const others = here.filter((a) => parentRow(a.uid) === undefined).sort(creatorFirst);
      let next = 0;
      for (const a of children) {
        const r = Math.max(next, parentRow(a.uid)!);
        row.set(a.uid, r);
        next = r + 1;
      }
      for (const a of others) row.set(a.uid, next++);
      rows = Math.max(rows, next);
    }
    const box = { x: MARGIN, y: top, w: areaW, h: LABEL + PAD * 2 + Math.max(1, rows) * STEP_Y - GAP_Y };
    areas.push({ projectUID, home, ...box });
    for (const a of members) {
      cards.push({
        uid: a.uid,
        projectUID: a.projectUID,
        column: column(a.uid),
        x: round(box.x + PAD + column(a.uid) * STEP_X),
        y: round(box.y + LABEL + PAD + row.get(a.uid)! * STEP_Y),
        w: CARD_W,
        h: CARD_H,
      });
    }
    top += box.h + AREA_GAP;
  };

  placeArea(
    input.project,
    true,
    agents.filter((a) => a.projectUID === input.project),
  );
  const outside = [...new Set(agents.map((a) => a.projectUID).filter((p) => p !== input.project))].sort(byString);
  for (const projectUID of outside) {
    placeArea(
      projectUID,
      false,
      agents.filter((a) => a.projectUID === projectUID),
    );
  }

  const at = new Map(cards.map((c) => [c.uid, c]));
  const home = areas[0];
  const cx = home.x + home.w / 2;
  const cy = home.y + home.h / 2;
  const edges: EdgePath[] = [];
  const sorted = [...input.edges].sort((x, y) => byString(x.a, y.a) || byString(x.b, y.b) || byString(x.kind, y.kind));
  for (const edge of sorted) {
    const p = at.get(edge.a);
    const q = at.get(edge.b);
    if (!p || !q || edge.a === edge.b) continue;
    if (edge.kind === "conversation") edges.push({ kind: "conversation", a: edge.a, b: edge.b, d: conversationPath(p, q, cx, cy) });
    else if (edge.kind === "created") edges.push({ kind: "created", a: edge.a, b: edge.b, d: createdPath(p, q) });
  }

  const width = home.x + home.w + MARGIN;
  const height = Math.max(...areas.map((a) => a.y + a.h)) + MARGIN;
  return { width, height, cards, areas, edges };
}

/**
 * A conversation: one quadratic curve. Between two cards of one column it
 * leaves both from their right sides and bows into the gap beside the column,
 * so it is not hidden under the cards between them; otherwise it runs centre
 * to centre, bending toward the middle of this Project's area.
 */
function conversationPath(p: CardBox, q: CardBox, cx: number, cy: number): string {
  if (p.column === q.column) {
    const x = p.x + p.w;
    const y1 = p.y + p.h / 2;
    const y2 = q.y + q.h / 2;
    // The peak is half the control point's offset.
    const peak = Math.min(GAP_X / 2 - 6, 14 + Math.abs(y2 - y1) * 0.08);
    return `M${round(x)} ${round(y1)} Q${round(x + peak * 2)} ${round((y1 + y2) / 2)} ${round(x)} ${round(y2)}`;
  }
  const x1 = p.x + p.w / 2;
  const y1 = p.y + p.h / 2;
  const x2 = q.x + q.w / 2;
  const y2 = q.y + q.h / 2;
  const dx = x2 - x1;
  const dy = y2 - y1;
  const len = Math.hypot(dx, dy) || 1;
  let nx = -dy / len;
  let ny = dx / len;
  const mx = (x1 + x2) / 2;
  const my = (y1 + y2) / 2;
  if ((cx - mx) * nx + (cy - my) * ny < 0) {
    nx = -nx;
    ny = -ny;
  }
  const peak = Math.min(90, Math.max(52, len * 0.12));
  const qx = mx + nx * peak * 2;
  const qy = my + ny * peak * 2;
  return `M${round(x1)} ${round(y1)} Q${round(qx)} ${round(qy)} ${round(x2)} ${round(y2)}`;
}

/**
 * Who created whom: a cubic curve that leaves the creator's side facing the
 * child and meets the child's side facing the creator, level at both ends so
 * the arrowhead points into the card. Two cards of one column are joined
 * along their left sides, bowing left, away from any conversation line.
 */
function createdPath(p: CardBox, q: CardBox): string {
  const py = p.y + p.h / 2;
  const qy = q.y + q.h / 2;
  let x1: number;
  let x2: number;
  let c1: number;
  let c2: number;
  if (q.column > p.column) {
    x1 = p.x + p.w;
    x2 = q.x;
    const k = Math.max(24, (x2 - x1) / 2);
    c1 = x1 + k;
    c2 = x2 - k;
  } else if (q.column < p.column) {
    x1 = p.x;
    x2 = q.x + q.w;
    const k = Math.max(24, (x1 - x2) / 2);
    c1 = x1 - k;
    c2 = x2 + k;
  } else {
    x1 = p.x;
    x2 = q.x;
    const k = Math.min(GAP_X / 2 + 8, 16 + Math.abs(qy - py) * 0.08);
    c1 = x1 - k;
    c2 = x2 - k;
  }
  return `M${round(x1)} ${round(py)} C${round(c1)} ${round(py)} ${round(c2)} ${round(qy)} ${round(x2)} ${round(qy)}`;
}
