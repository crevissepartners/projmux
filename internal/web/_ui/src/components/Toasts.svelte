<script lang="ts">
  import { t } from "../lib/i18n.svelte";
  import { dismiss, toasts } from "../lib/toast.svelte";
</script>

<div id="toasts">
  {#each toasts as item (item.id)}
    <div class="toast show {item.tone}">
      <button type="button" class="toast-text" onclick={() => dismiss(item.id)}>{item.text}</button>
      {#if item.detail && item.detail !== item.text}
        <details class="toast-detail">
          <summary>{t("web.error.details")}</summary>
          <pre>{item.detail}</pre>
        </details>
      {/if}
    </div>
  {/each}
</div>
