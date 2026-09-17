<script lang="ts">
  // Alt-4, the terminal's resume picker: this window's agents whose pane is
  // gone, each with the first and last line of its conversation, and a row
  // that starts something new instead.
  import { get, paths, post } from "../lib/api";
  import { phaseText, providerText } from "../lib/errors";
  import { t } from "../lib/i18n.svelte";
  import { go, route } from "../lib/router.svelte";
  import { refresh } from "../lib/state.svelte";
  import { ago } from "../lib/time";
  import { fail } from "../lib/toast.svelte";
  import type { ResumeCandidate } from "../lib/types";
  import { ui } from "../lib/ui.svelte";
  import ListPicker, { type ListRow } from "./ListPicker.svelte";

  let { onClose }: { onClose: () => void } = $props();

  const sel = route.sel;
  let candidates = $state<ResumeCandidate[]>([]);
  let loaded = $state(false);
  let showEmpty = $state(false);
  let resuming = $state("");
  if (sel.window) {
    get<{ items: ResumeCandidate[] }>(paths.resumeCandidates(sel.window))
      .then((body) => (candidates = body.items))
      .catch(fail)
      .finally(() => (loaded = true));
  }

  // A candidate with no conversation has nothing to recognize it by; those
  // are folded behind one line.
  const hasHistory = (c: ResumeCandidate) => !!(c.opening || c.last) || c.note === "empty";
  const empties = $derived(candidates.filter((c) => !hasHistory(c)));

  const rows = $derived.by((): ListRow[] => {
    const out: ListRow[] = [
      { key: "new", name: t("web.resume.new"), status: "NEW", tone: "info", search: "new launch" },
    ];
    for (const c of candidates) {
      if (!hasHistory(c) && !showEmpty) continue;
      const name = c.name === c.uid ? providerText(c.provider) : c.name;
      const lines: ListRow["lines"] = [];
      if (c.opening) lines.push({ text: c.opening, kind: "open" });
      if (c.last && c.last !== c.opening) lines.push({ text: c.last, kind: "last" });
      if (c.note === "empty") lines.push({ text: t("web.picker.note.empty"), kind: "note" });
      out.push({
        key: c.uid,
        name,
        detail: [providerText(c.provider), phaseText(c.phase), c.turns ? t("web.picker.turns", { n: c.turns }) : "", ago(c.at)]
          .filter(Boolean)
          .join(" · "),
        lines,
        search: `${name} ${c.provider} ${c.opening || ""} ${c.last || ""}`,
      });
    }
    if (empties.length) {
      out.push({
        key: "toggle-empty",
        name: showEmpty ? t("web.picker.hide_empty") : t("web.picker.show_empty", { n: empties.length }),
        search: "",
      });
    }
    return out;
  });

  async function pick(row: ListRow) {
    if (row.key === "new") {
      ui.overlay = "launch";
      return;
    }
    if (row.key === "toggle-empty") {
      showEmpty = !showEmpty;
      return;
    }
    resuming = row.key;
    try {
      await post(`${paths.agent(row.key)}/resume`, { confirm: true });
      await refresh();
      onClose();
      if (sel.project && sel.window) go({ project: sel.project, window: sel.window, pane: row.key });
    } catch (err) {
      fail(err);
    } finally {
      resuming = "";
    }
  }
</script>

<ListPicker title={t("web.resume.title")} hint="Alt-4" prompt="AI Resume >" {rows} busy={resuming} onPick={pick} {onClose}>
  {#snippet footer()}
    {#if !sel.window}
      <span class="warn">{t("web.resume.no_window")}</span>
    {:else if loaded && !candidates.length}
      <span>{t("web.resume.none")}</span>
    {:else}
      <span>{t("web.resume.hint")}</span>
    {/if}
  {/snippet}
</ListPicker>
