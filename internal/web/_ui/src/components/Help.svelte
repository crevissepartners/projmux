<script lang="ts">
  import { t } from "../lib/i18n.svelte";
  import Picker from "./Picker.svelte";

  let { onClose }: { onClose: () => void } = $props();

  // Keys follow projmux's own where there is one (docs/keybindings.md).
  const keys = $derived([
    ["Ctrl-K", t("web.help.switcher")],
    ["Alt-1", t("web.help.projects")],
    ["Alt-2", t("web.help.notify")],
    ["Alt-7", t("web.help.split")],
    ["Alt-4", t("web.help.resume")],
    ["Alt-5", t("web.help.settings")],
    ["Alt-N", t("web.help.new_window")],
    ["Alt-←/→/↑/↓", t("web.help.panes")],
    ["Alt-Shift-←/→", t("web.help.windows")],
    ["Ctrl-Enter", t("web.help.send")],
    ["r", t("web.help.refresh")],
    ["Esc", t("web.help.escape")],
    [t("web.help.drag_key"), t("web.help.drag")],
  ]);
</script>

<Picker title={t("web.tools.help")} hint="?" wide {onClose}>
  <dl class="help-list">
    {#each keys as [key, what] (key)}
      <dt>{key}</dt>
      <dd>{what}</dd>
    {/each}
  </dl>
</Picker>
