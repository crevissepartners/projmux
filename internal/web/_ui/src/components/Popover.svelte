<script lang="ts">
  // A button that opens a small panel beside it. The panel is placed with
  // fixed coordinates so a slot's overflow does not clip it, and it closes on
  // Escape, on a click outside, or on the button again.
  import { tick, type Snippet } from "svelte";

  interface Props {
    /** The button's face: text, or a snippet when it is more than text. */
    face?: string;
    faceSnippet?: Snippet;
    title: string;
    className?: string;
    align?: "start" | "end";
    /** Where the panel opens; the status bar opens upward. */
    side?: "below" | "above";
    wide?: boolean;
    children: Snippet;
  }
  let {
    face = "",
    faceSnippet,
    title,
    className = "slot-btn",
    align = "end",
    side = "below",
    wide = false,
    children,
  }: Props = $props();

  let open = $state(false);
  let button: HTMLButtonElement | undefined = $state();
  let panel: HTMLDivElement | undefined = $state();
  let place = $state("");

  async function toggle() {
    open = !open;
    if (!open || !button) return;
    const r = button.getBoundingClientRect();
    const vertical = side === "below" ? `top:${r.bottom + 4}px` : `bottom:${window.innerHeight - r.top + 4}px`;
    place =
      align === "end"
        ? `${vertical};right:${Math.max(8, window.innerWidth - r.right)}px`
        : `${vertical};left:${Math.max(8, r.left)}px`;
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

<svelte:window onmousedowncapture={outside} onkeydown={key} onresize={() => close(false)} />

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
  }}
  >{#if faceSnippet}{@render faceSnippet()}{:else}{face}{/if}</button
>
{#if open}
  <div class="popover" class:wide role="dialog" aria-label={title} tabindex="-1" style={place} bind:this={panel} onmousedown={(e) => e.stopPropagation()}>
    <div class="popover-title">{title}</div>
    {@render children()}
  </div>
{/if}
