<script lang="ts">
  // The pane level is a split, not a tab strip. A Window's Panes are laid out
  // at once, each where tmux put it, so a real split shows up here as the view
  // splitting the same way. tmux reports geometry in cells; percentages of
  // the drawn frame reproduce any tmux layout, which is always nested binary
  // splits. Until the geometry arrives the panes sit in a row, which is the
  // same arrangement for the common case.
  import { load, save } from "../lib/local";
  import { geometry, type Box } from "../lib/geometry.svelte";
  import type { PaneView, ProjectView, WindowView } from "../lib/tree";
  import Slot from "./Slot.svelte";

  interface Props {
    project: ProjectView;
    win: WindowView;
    panes: PaneView[];
    focused: string | null;
  }
  let { project, win, panes, focused }: Props = $props();

  let container: HTMLElement | undefined = $state();

  interface Bar {
    axis: "x" | "y";
    cell: number;
    key: string;
    cells: number[];
  }

  const boxes = $derived(panes.map((p) => geometry.panes[p.runtimeId] as Box | undefined));
  const usable = $derived(
    geometry.window === win.uid && panes.length > 0 && boxes.every((b): b is Box => !!b),
  );

  // The frame is the bounding box of the panes actually drawn, not the tmux
  // window: hidden shell panes and a top status row would otherwise leave
  // space that belongs to nothing on screen.
  const frame = $derived.by(() => {
    if (!usable) return null;
    const list = boxes as Box[];
    const left = Math.min(...list.map((b) => b.x));
    const top = Math.min(...list.map((b) => b.y));
    const width = Math.max(...list.map((b) => b.x + b.width)) - left;
    const height = Math.max(...list.map((b) => b.y + b.height)) - top;
    return width > 0 && height > 0 ? { left, top, width, height } : null;
  });

  // A boundary is where panes on one side end and panes on the other begin.
  // tmux leaves a separator cell between them, so a boundary governs two cell
  // coordinates and both must move together.
  const bars = $derived.by((): Bar[] => {
    if (!frame) return [];
    const list = boxes as Box[];
    const out: Bar[] = [];
    for (const [axis, start, size, min] of [
      ["x", "x", "width", frame.left],
      ["y", "y", "height", frame.top],
    ] as const) {
      const seen = new Set<number>();
      for (const box of list) {
        const cell = box[start];
        if (cell <= min || seen.has(cell)) continue;
        const ends = list.map((o) => o[start] + o[size]).filter((end) => end <= cell && end > cell - 3);
        if (!ends.length) continue;
        seen.add(cell);
        const starts = list.map((o) => o[start]).filter((s) => s <= cell && s > cell - 3);
        out.push({ axis, cell, key: `${axis}${cell}`, cells: [...new Set([...ends, ...starts, cell])] });
      }
    }
    return out;
  });

  // Dragged boundaries are this browser's reading of the layout; the real
  // tmux layout is untouched.
  let shifts = $state<Record<string, number>>({});
  $effect(() => {
    shifts = load(`split.${geometry.tmuxWindow}`, {});
  });

  const cellKey = $derived.by(() => {
    const keys = { x: {} as Record<number, string>, y: {} as Record<number, string> };
    for (const bar of bars) for (const cell of bar.cells) keys[bar.axis][cell] = bar.key;
    return keys;
  });

  function pct(axis: "x" | "y", cell: number): number {
    if (!frame) return 0;
    const base = axis === "x" ? ((cell - frame.left) / frame.width) * 100 : ((cell - frame.top) / frame.height) * 100;
    return base + (shifts[cellKey[axis][cell]] || 0);
  }

  function place(index: number): string {
    const box = boxes[index];
    if (!usable || !box) return "";
    const x0 = pct("x", box.x);
    const y0 = pct("y", box.y);
    return `left:${x0}%;top:${y0}%;width:${pct("x", box.x + box.width) - x0}%;height:${pct("y", box.y + box.height) - y0}%`;
  }

  function drag(event: PointerEvent, bar: Bar) {
    event.preventDefault();
    event.stopPropagation();
    const handle = event.currentTarget as HTMLElement;
    handle.setPointerCapture(event.pointerId);
    const rect = container!.getBoundingClientRect();
    const total = bar.axis === "x" ? rect.width : rect.height;
    const start = bar.axis === "x" ? event.clientX : event.clientY;
    const base = shifts[bar.key] || 0;
    document.body.classList.add(bar.axis === "x" ? "resizing" : "resizing-y");
    const move = (e: PointerEvent) => {
      const now = bar.axis === "x" ? e.clientX : e.clientY;
      // Clamped so a boundary cannot be pushed past its neighbours.
      shifts[bar.key] = Math.max(-40, Math.min(40, base + ((now - start) / total) * 100));
    };
    const stop = () => {
      handle.removeEventListener("pointermove", move);
      handle.removeEventListener("pointerup", stop);
      handle.removeEventListener("pointercancel", stop);
      document.body.classList.remove("resizing", "resizing-y");
      save(`split.${geometry.tmuxWindow}`, $state.snapshot(shifts));
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", stop);
    handle.addEventListener("pointercancel", stop);
  }

  function reset(bar: Bar) {
    delete shifts[bar.key];
    save(`split.${geometry.tmuxWindow}`, $state.snapshot(shifts));
  }
</script>

<div class="content panes" class:placed={!!frame} id="detail" bind:this={container}>
  {#each panes as pane, index (pane.uid)}
    <Slot {project} {win} {pane} {index} focused={pane.uid === focused} style={frame ? place(index) : ""} />
  {/each}
  {#if frame}
    {#each bars as bar (bar.key)}
      <div
        class="split-bar {bar.axis}"
        style={bar.axis === "x" ? `left:${pct("x", bar.cell)}%` : `top:${pct("y", bar.cell)}%`}
        onpointerdown={(e) => drag(e, bar)}
        ondblclick={(e) => {
          e.stopPropagation();
          reset(bar);
        }}
        role="separator"
        aria-orientation={bar.axis === "x" ? "vertical" : "horizontal"}
      ></div>
    {/each}
  {/if}
</div>
