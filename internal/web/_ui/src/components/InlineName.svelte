<script lang="ts">
  // Rename in place, where the name is. Enter commits, Escape or leaving the
  // field abandons. The label keeps its old text until the Registry says
  // otherwise, so a refused name does not look accepted.
  interface Props {
    value: string;
    dim?: boolean;
    className?: string;
    title?: string;
    rename: (name: string) => Promise<void>;
  }
  let { value, dim = false, className = "name", title = "", rename }: Props = $props();
  let editing = $state(false);
  let draft = $state("");

  function start(event: MouseEvent) {
    event.preventDefault();
    event.stopPropagation();
    draft = dim ? "" : value;
    editing = true;
  }

  function focus(node: HTMLInputElement) {
    node.focus();
    node.select();
  }

  async function finish(commit: boolean) {
    if (!editing) return;
    editing = false;
    const name = draft.trim();
    if (commit && name && name !== value) await rename(name);
  }
</script>

{#if editing}
  <span class={className}>
    <input
      class="rename"
      size={Math.max(6, draft.length + 2)}
      bind:value={draft}
      use:focus
      onkeydown={(e) => {
        e.stopPropagation();
        if (e.key === "Enter") finish(true);
        if (e.key === "Escape") finish(false);
      }}
      onblur={() => finish(false)}
      onclick={(e) => e.stopPropagation()}
    />
  </span>
{:else}
  <span class={dim ? `${className} dim` : className} {title} ondblclick={start} role="textbox" tabindex="-1">{value}</span>
{/if}
