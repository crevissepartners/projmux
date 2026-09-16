<script lang="ts">
  // The native picker frame (docs/native-picker.md): a framed popup with a
  // title, a key hint, and whatever list the caller puts in it. A click on the
  // backdrop closes it.
  import type { Snippet } from "svelte";

  interface Props {
    title: string;
    hint: string;
    wide?: boolean;
    onClose: () => void;
    children: Snippet;
  }
  let { title, hint, wide = false, onClose, children }: Props = $props();
</script>

<div
  class="picker-overlay"
  role="presentation"
  onmousedown={(e) => {
    if (e.target === e.currentTarget) onClose();
  }}
>
  <div class="picker" class:help={wide} role="dialog" aria-label={title}>
    <div class="picker-head">
      <span>{title}</span>
      <span class="grow"></span>
      <kbd>{hint}</kbd>
    </div>
    {@render children()}
  </div>
</div>
