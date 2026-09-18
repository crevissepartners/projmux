// Where the Agent graph puts each card, as a pure function of the graph.
//
// The same agents and edges always give the same coordinates: the input is
// copied and sorted by uid (and outside Projects by their uid) before anything
// is placed, nothing is random, and nothing reads the clock. Two screenshots
// of the same graph can be compared, and a card does not jump when the page
// reads the graph again.
//
// This Project's cards go round the edge of a rectangle, in uid order,
// clockwise from the top left. A conversation between two of them is a chord
// across the empty middle, so a line rarely runs under a third card. Each
// outside Project is a labelled box of its own, stacked in columns to the
// right. Placement is not overlap-free for every conceivable graph; the page
// scrolls instead.

export const CARD_W = 208;
export const CARD_H = 76;
const GAP_X = 24;
const GAP_Y = 20;
const STEP_X = CARD_W + GAP_X;
const STEP_Y = CARD_H + GAP_Y;
/** Inside an area, around its cards. */
const PAD = 20;
/** An area's label row, above its cards. */
const LABEL = 24;
/** Between this Project's area and the outside boxes, and between those. */
const AREA_GAP = 40;
/** Around everything, so a curve or a shadow is never cut off. */
const MARGIN = 12;
/** The width-to-height ratio the frame of this Project's cards aims for. */
const ASPECT = 2;

export interface LayoutInput {
  /** The Project the graph is about. */
  project: string;
  agents: readonly { uid: string; projectUID: string }[];
  edges: readonly { a: string; b: string }[];
}

export interface CardBox {
  uid: string;
  projectUID: string;
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
  a: string;
  b: string;
  /** An SVG path: one quadratic curve from centre to centre. */
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

/** The frame's shape: `top` cards per long side, `side` cards per short side. */
function frame(n: number): { cols: number; rows: number; slots: [number, number][] } {
  if (n <= 3) {
    const cols = Math.max(1, n);
    return { cols, rows: 1, slots: Array.from({ length: n }, (_, i) => [i, 0] as [number, number]) };
  }
  let best = { top: 2, side: Math.ceil((n - 4) / 2), score: Infinity };
  for (let top = 2; top <= Math.ceil(n / 2); top++) {
    const side = Math.max(0, Math.ceil((n - 2 * top) / 2));
    const w = top * STEP_X - GAP_X;
    const h = (side + 2) * STEP_Y - GAP_Y;
    const score = Math.abs(w / h - ASPECT);
    if (score < best.score) best = { top, side, score };
  }
  const { top, side } = best;
  const slots: [number, number][] = [];
  for (let i = 0; i < top; i++) slots.push([i, 0]);
  for (let j = 1; j <= side; j++) slots.push([top - 1, j]);
  for (let i = top - 1; i >= 0; i--) slots.push([i, side + 1]);
  for (let j = side; j >= 1; j--) slots.push([0, j]);
  return { cols: top, rows: side + 2, slots };
}

function grid(cols: number, rows: number) {
  return {
    w: PAD * 2 + cols * STEP_X - GAP_X,
    h: LABEL + PAD * 2 + rows * STEP_Y - GAP_Y,
  };
}

export function layoutAgentGraph(input: LayoutInput): AgentGraphLayout {
  const agents = [...input.agents].sort((x, y) => byString(x.uid, y.uid));
  const cards: CardBox[] = [];
  const areas: AreaBox[] = [];
  const place = (uid: string, projectUID: string, x: number, y: number) =>
    cards.push({ uid, projectUID, x: round(x), y: round(y), w: CARD_W, h: CARD_H });

  // This Project, round the frame.
  const home = agents.filter((a) => a.projectUID === input.project);
  const shape = frame(home.length);
  const homeBox = { x: MARGIN, y: MARGIN, ...grid(shape.cols, shape.rows) };
  areas.push({ projectUID: input.project, home: true, ...homeBox });
  home.forEach((agent, i) => {
    const [col, row] = shape.slots[i];
    place(agent.uid, agent.projectUID, homeBox.x + PAD + col * STEP_X, homeBox.y + LABEL + PAD + row * STEP_Y);
  });

  // Every other Project, one box each, in columns to the right.
  const outside = new Map<string, string[]>();
  for (const agent of agents) {
    if (agent.projectUID === input.project) continue;
    const list = outside.get(agent.projectUID) || [];
    list.push(agent.uid);
    outside.set(agent.projectUID, list);
  }
  const groups = [...outside.keys()].sort(byString).map((projectUID) => {
    const members = outside.get(projectUID)!;
    const cols = Math.min(2, members.length);
    return { projectUID, members, cols, ...grid(cols, Math.ceil(members.length / cols)) };
  });
  const limit = Math.max(homeBox.h, ...groups.map((g) => g.h));
  let colX = homeBox.x + homeBox.w + AREA_GAP;
  let colW = 0;
  let y = MARGIN;
  for (const group of groups) {
    if (y > MARGIN && y - MARGIN + group.h > limit) {
      colX += colW + AREA_GAP;
      colW = 0;
      y = MARGIN;
    }
    areas.push({ projectUID: group.projectUID, home: false, x: colX, y, w: group.w, h: group.h });
    group.members.forEach((uid, i) => {
      const col = i % group.cols;
      const row = Math.floor(i / group.cols);
      place(uid, group.projectUID, colX + PAD + col * STEP_X, y + LABEL + PAD + row * STEP_Y);
    });
    y += group.h + AREA_GAP;
    colW = Math.max(colW, group.w);
  }

  // Edges bend toward the middle of this Project's frame, so a line between
  // two neighbours shows beside them instead of hiding under both cards.
  const at = new Map(cards.map((c) => [c.uid, c]));
  const cx = homeBox.x + homeBox.w / 2;
  const cy = homeBox.y + homeBox.h / 2;
  const edges: EdgePath[] = [];
  const sorted = [...input.edges].sort((x, y) => byString(x.a, y.a) || byString(x.b, y.b));
  for (const edge of sorted) {
    const p = at.get(edge.a);
    const q = at.get(edge.b);
    if (!p || !q || edge.a === edge.b) continue;
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
    // The curve's peak is half the control point's offset.
    const peak = Math.min(90, Math.max(52, len * 0.12));
    const qx = mx + nx * peak * 2;
    const qy = my + ny * peak * 2;
    edges.push({
      a: edge.a,
      b: edge.b,
      d: `M${round(x1)} ${round(y1)} Q${round(qx)} ${round(qy)} ${round(x2)} ${round(y2)}`,
    });
  }

  const width = Math.max(...areas.map((a) => a.x + a.w)) + MARGIN;
  const height = Math.max(...areas.map((a) => a.y + a.h)) + MARGIN;
  return { width, height, cards, areas, edges };
}
