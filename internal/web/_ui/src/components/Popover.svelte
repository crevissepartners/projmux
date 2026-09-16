<script lang="ts">
  // A button that opens a small panel beside it. The panel is placed with
  // fixed coordinates so a slot's overflow does not clip it, and it closes on
  // Escape, on a click outside, or on the button again.
  import { tick, type Snippet } from "svelte";

  interface Props {
    /** The button's face. */
    face: string;
    title: string;
    className?: string;
    align?: "start" | "end";
    children: Snippet;
  }
  let { face, title, className = "slot-btn", align = "end", children }: Props = $props();

  let open = $state(false);
  let button: HTMLButtonElement | undefined = $state();
  let panel: HTMLDivElement | undefined = $state();
  let place = $state("");

  async function toggle() {
    open = !open;
    if (!open || !button) return;
    const r = button.getBoundingClientRect();
    const top = r.bottom + 4;
    place =
      align === "end"
        ? `top:${top}px;right:${Math.max(8, window.innerWidth - r.right)}px`
        : `top:${top}px;left:${Math.max(8, r.left)}px`;
    await tick();
    panel?.focus();
  }

  function close(refocus: boolean) {
    open = false;
    if (refocus) button?.focus();
  }

  function outside(event: MouseEvent) {
    const target = event.target as Node;
    if (open && !panel?.contains(target) && !button?.contains(target)) close(false);
  }

  function key(event: KeyboardEvent) {
    if (open && event.key === "Escape") {
      event.stopPropagation();
      event.preventDefault();
      close(true);
    }
  }
</script>

<svelte:window onmousedown={outside} onkeydown={key} onresize={() => close(false)} />

<button
  type="button"
  class={className}
  {title}
  aria-label={title}
  aria-expanded={open}
  aria-haspopup="dialog"
  bind:this={button}
  onmousedown={(e) => e.stopPropagation()}
  onclick={(e) => {
    e.stopPropagation();
    toggle();
  }}>{face}</button
>
{#if open}
  <div class="popover" role="dialog" aria-label={title} tabindex="-1" style={place} bind:this={panel} onmousedown={(e) => e.stopPropagation()}>
    <div class="popover-title">{title}</div>
    {@render children()}
  </div>
{/if}
