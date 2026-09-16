import { drop, load, save } from "./local";

/**
 * Drag a sidebar by its inner edge. The width is this browser's preference,
 * not shared state. Double-click returns to the stylesheet's width.
 */
export function resizable(panel: HTMLElement, opts: { key: string; edge: "left" | "right" }) {
  const handle = document.createElement("div");
  handle.className = `resizer ${opts.edge}`;
  panel.append(handle);

  // An explicit width has to clear the stylesheet's min/max, or a drag stops
  // dead at a bound the person did not set.
  const setWidth = (px: number) => {
    panel.style.width = `${px}px`;
    panel.style.minWidth = "0";
    panel.style.maxWidth = "none";
  };
  const stored = load(opts.key, 0);
  if (stored) setWidth(stored);

  // Pointer capture keeps the drag alive when the pointer leaves the handle,
  // which is the normal case when dragging fast.
  const down = (event: PointerEvent) => {
    event.preventDefault();
    handle.setPointerCapture(event.pointerId);
    const startX = event.clientX;
    const startWidth = panel.getBoundingClientRect().width;
    document.body.classList.add("resizing");
    const move = (e: PointerEvent) => {
      const delta = opts.edge === "right" ? e.clientX - startX : startX - e.clientX;
      setWidth(Math.max(180, Math.min(620, startWidth + delta)));
    };
    const stop = () => {
      handle.removeEventListener("pointermove", move);
      handle.removeEventListener("pointerup", stop);
      handle.removeEventListener("pointercancel", stop);
      document.body.classList.remove("resizing");
      save(opts.key, Math.round(panel.getBoundingClientRect().width));
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", stop);
    handle.addEventListener("pointercancel", stop);
  };
  const reset = () => {
    panel.style.width = "";
    panel.style.minWidth = "";
    panel.style.maxWidth = "";
    drop(opts.key);
  };
  handle.addEventListener("pointerdown", down);
  handle.addEventListener("dblclick", reset);
  return {
    destroy() {
      handle.remove();
    },
  };
}

/**
 * Keep a log pinned to its newest line until the reader scrolls away.
 *
 * Scrolling once after rendering does not work: a slot is sized after its
 * content is built, from a percentage of a container whose geometry arrives
 * from tmux later. Observing content and size instead keeps "at the bottom"
 * true through late layout, streamed turns and fonts settling. Scrolling up
 * unpins; coming back to the bottom re-pins.
 */
export function stickToBottom(log: HTMLElement) {
  let pinned = true;
  const toEnd = () => {
    if (pinned) log.scrollTop = log.scrollHeight;
  };
  const onScroll = () => {
    pinned = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
  };
  log.addEventListener("scroll", onScroll);
  const mutations = new MutationObserver(toEnd);
  mutations.observe(log, { childList: true, subtree: true, characterData: true });
  const sizes = new ResizeObserver(toEnd);
  sizes.observe(log);
  toEnd();
  return {
    destroy() {
      log.removeEventListener("scroll", onScroll);
      mutations.disconnect();
      sizes.disconnect();
    },
  };
}

/** Arrow keys inside a sidebar list, the way the tmux sidebars take them. */
export function listKeys(list: HTMLElement, onEscape: () => void) {
  const keydown = (event: KeyboardEvent) => {
    if (event.altKey || event.ctrlKey || event.metaKey) return;
    const rows = [...list.querySelectorAll<HTMLButtonElement>("button.row:not(:disabled)")];
    if (!rows.length) return;
    const at = rows.indexOf(document.activeElement as HTMLButtonElement);
    let next = -1;
    if (event.key === "ArrowDown" || event.key === "j") next = at < 0 ? 0 : Math.min(rows.length - 1, at + 1);
    else if (event.key === "ArrowUp" || event.key === "k") next = at < 0 ? 0 : Math.max(0, at - 1);
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = rows.length - 1;
    else if (event.key === "Escape") {
      event.preventDefault();
      onEscape();
      return;
    }
    if (next < 0) return;
    event.preventDefault();
    rows[next].focus();
    rows[next].scrollIntoView({ block: "nearest" });
  };
  list.addEventListener("keydown", keydown);
  return {
    destroy() {
      list.removeEventListener("keydown", keydown);
    },
  };
}

/** Put focus on a list's current row, or its first. */
export function focusList(list: HTMLElement | undefined): void {
  requestAnimationFrame(() => {
    const row =
      list?.querySelector<HTMLButtonElement>('button.row[aria-current="true"]') ||
      list?.querySelector<HTMLButtonElement>("button.row:not(:disabled)");
    row?.focus();
  });
}
