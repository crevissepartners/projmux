<script lang="ts">
  interface Props {
    label: string;
    pct: number;
    stale?: boolean;
    /** The value came from the fallback source; the label is tinted, as in the terminal. */
    fallback?: boolean;
    title?: string;
  }
  let { label, pct, stale = false, fallback = false, title = "" }: Props = $props();
  const level = $derived(pct >= 90 ? "full" : pct >= 70 ? "hot" : "");
</script>

<span class="meter {level}" class:stale class:fallback {title}>
  <span class="label">{label}</span>
  <span class="bar"><span class="fill" style:width="{Math.max(0, Math.min(100, pct))}%"></span></span>
  <span class="pct">{pct}%</span>
</span>
