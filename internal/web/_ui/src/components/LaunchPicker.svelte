<script lang="ts">
  // Alt-7, the terminal's AI launch picker: each enabled provider, Claude's
  // advanced launch (model and effort), and a plain shell. Picking one splits
  // it to the right of the focused pane at once.
  import { t } from "../lib/i18n.svelte";
  import { launch, launchAnchor, launcher, loadLaunchOptions } from "../lib/launch.svelte";
  import { ui } from "../lib/ui.svelte";
  import ListPicker, { type ListRow } from "./ListPicker.svelte";

  let { advanced = false, onClose }: { advanced?: boolean; onClose: () => void } = $props();

  loadLaunchOptions();
  const anchored = $derived(!!launchAnchor());

  const rows = $derived.by((): ListRow[] => {
    const options = launcher.options;
    if (!options) return [];
    if (advanced) {
      const out: ListRow[] = [];
      for (const model of ["", ...options.claudeModels]) {
        for (const effort of ["", ...options.claudeEfforts]) {
          const name = `${model || t("web.picker.default")} · ${effort || t("web.picker.default")}`;
          out.push({ key: `${model}|${effort}`, name, search: `${model} ${effort} ${name}` });
        }
      }
      return out;
    }
    const out: ListRow[] = [];
    for (const provider of options.providers) {
      out.push({
        key: provider.id,
        name: provider.id,
        status: provider.ready ? "READY" : "MISSING",
        tone: provider.ready ? "ok" : "warn",
        detail: t("web.launch.split", { name: provider.name }),
        search: `${provider.id} ${provider.name}`,
        disabled: !provider.ready,
      });
      if (provider.id === "claude" && provider.ready) {
        out.push({
          key: "claude+",
          name: "claude+",
          status: "ADVANCED",
          tone: "info",
          detail: t("web.launch.advanced"),
          search: "claude advanced model effort",
        });
      }
    }
    out.push({ key: "shell", name: "shell", status: "READY", tone: "ok", detail: t("web.launch.shell"), search: "shell plain" });
    return out;
  });

  async function pick(row: ListRow) {
    if (row.key === "claude+") {
      ui.overlay = "launch-advanced";
      return;
    }
    const [model, effort] = advanced ? row.key.split("|") : ["", ""];
    if (await launch(advanced ? "claude" : row.key, { model, effort })) onClose();
  }
</script>

<ListPicker
  title={advanced ? t("web.launch.advanced_title") : t("web.launch.title")}
  hint="Alt-7"
  prompt={advanced ? "Claude >" : "AI Launch >"}
  {rows}
  busy={launcher.running ? (advanced ? "" : launcher.running) : ""}
  onPick={pick}
  {onClose}
>
  {#snippet footer()}
    {#if !anchored}
      <span class="warn">{t("web.picker.pick_pane_first")}</span>
    {:else if launcher.running}
      <span>{t("web.picker.running")}</span>
    {:else if launcher.options && !launcher.options.providers.length}
      <span>{t("web.launch.none_enabled")}</span>
    {:else}
      <span>{advanced ? t("web.launch.advanced_hint") : t("web.launch.hint")}</span>
    {/if}
  {/snippet}
</ListPicker>
