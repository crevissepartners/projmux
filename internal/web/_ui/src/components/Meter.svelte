<script lang="ts">
  interface Props {
    label: string;
    pct: number;
    stale?: boolean;
    title?: string;
  }
  let { label, pct, stale = false, title = "" }: Props = $props();
  const level = $derived(pct >= 90 ? "full" : pct >= 70 ? "hot" : "");
</script>

<span class="meter {level}" class:stale {title}>
  <span class="label">{label}</span>
  <span class="bar"><span class="fill" style:width="{Math.max(0, Math.min(100, pct))}%"></span></span>
  <span class="pct">{pct}%</span>
</span>
