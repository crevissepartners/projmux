<script lang="ts">
  import { copyText } from "../lib/clipboard";
  import { t } from "../lib/i18n.svelte";

  let { text }: { text: string } = $props();
  let state = $state<"" | "done" | "failed">("");
  let timer: ReturnType<typeof setTimeout> | undefined;

  async function copy() {
    state = (await copyText(text)) ? "done" : "failed";
    clearTimeout(timer);
    timer = setTimeout(() => (state = ""), 1500);
  }
</script>

<button type="button" class="copy-btn" class:done={state === "done"} onclick={copy}>
  {state === "done" ? t("web.copy.done") : state === "failed" ? t("web.copy.failed") : t("web.copy.label")}
</button>
