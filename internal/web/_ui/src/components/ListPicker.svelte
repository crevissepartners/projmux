<script lang="ts" module>
  export interface ListRow {
    key: string;
    name: string;
    /** A short state tag: READY, MISSING, ADVANCED. */
    status?: string;
    tone?: "" | "ok" | "warn" | "info";
    detail?: string;
    /** Extra lines under the row, such as a conversation's first and last. */
    lines?: { text: string; kind: string }[];
    search: string;
    disabled?: boolean;
  }
</script>

<script lang="ts">
  // The terminal picker's shape: a prompt that filters, one list, arrow keys to
  // move, Enter to pick, Escape to close.
  import type { Snippet } from "svelte";
  import Picker from "./Picker.svelte";

  interface Props {
    title: string;
    hint: string;
    prompt: string;
    rows: ListRow[];
    busy?: string;
    footer?: Snippet;
    onPick: (row: ListRow) => void;
    onClose: () => void;
  }
  let { title, hint, prompt, rows, busy = "", footer, onPick, onClose }: Props = $props();

  let query = $state("");
  let selected = $state(0);
  let input: HTMLInputElement | undefined = $state();
  let list: HTMLElement | undefined = $state();
  $effect(() => input?.focus());

  const shown = $derived.by(() => {
    const needle = query.trim().toLowerCase();
    return needle ? rows.filter((row) => row.search.toLowerCase().includes(needle)) : rows;
  });
  $effect(() => {
    void query;
    selected = 0;
  });

  function move(step: number) {
    if (!shown.length) return;
    selected = (selected + step + shown.length) % shown.length;
    list?.querySelectorAll(".picker-row")[selected]?.scrollIntoView({ block: "nearest" });
  }

  function pick(row: ListRow | undefined) {
    if (row && !row.disabled && !busy) onPick(row);
  }
</script>

<Picker {title} {hint} {onClose}>
  <label class="picker-prompt">
    <span>{prompt}</span>
    <input
      bind:this={input}
      bind:value={query}
      onkeydown={(e) => {
        if (e.key === "ArrowDown") {
          e.preventDefault();
          move(1);
        } else if (e.key === "ArrowUp") {
          e.preventDefault();
          move(-1);
        } else if (e.key === "Enter") {
          e.preventDefault();
          pick(shown[selected]);
        } else if (e.key === "Escape") {
          e.preventDefault();
          onClose();
        }
      }}
    />
  </label>
  <div class="picker-list" bind:this={list} role="listbox">
    {#each shown as row, i (row.key)}
      <button
        type="button"
        class="picker-row launch-row"
        class:selected={i === selected}
        class:busy={busy === row.key}
        disabled={row.disabled || (!!busy && busy !== row.key)}
        role="option"
        aria-selected={i === selected}
        onmouseenter={() => (selected = i)}
        onclick={() => pick(row)}
      >
        <div class="launch-head">
          <span class="name">{row.name}</span>
          {#if row.status}<span class="launch-status {row.tone || ''}">{row.status}</span>{/if}
          {#if row.detail}<span class="where">{row.detail}</span>{/if}
        </div>
        {#each row.lines || [] as line, j (j)}
          <div class="resume-line {line.kind}">{line.text}</div>
        {/each}
      </button>
    {/each}
  </div>
  {#if footer}<div class="picker-foot">{@render footer()}</div>{/if}
</Picker>
