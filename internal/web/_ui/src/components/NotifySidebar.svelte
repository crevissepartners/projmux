<script lang="ts">
  import { paths, post } from "../lib/api";
  import { t } from "../lib/i18n.svelte";
  import { go } from "../lib/router.svelte";
  import { live } from "../lib/state.svelte";
  import { ago } from "../lib/time";
  import { fail } from "../lib/toast.svelte";
  import { paneByRuntime, slotRef } from "../lib/tree";
  import { listKeys, resizable } from "../lib/actions";
  import type { Notification } from "../lib/types";

  interface Props {
    list?: HTMLElement;
    onEscape: () => void;
  }
  let { list = $bindable(), onEscape }: Props = $props();

  const critical = $derived(live.notifications.filter((n) => n.severity === "critical").length);
  const busy = new Set<string>();

  const expired = (n: Notification) => !!n.expires_at && new Date(n.expires_at).getTime() < Date.now();

  /**
   * Selecting a row does what it does in projmux: ack the notification and
   * focus its pane, in the operator's real tmux client too. A row whose pane
   * is not found still acks.
   */
  async function select(entry: Notification) {
    if (busy.has(entry.id)) return;
    busy.add(entry.id);
    try {
      const target = paneByRuntime(live.tree, entry.pane);
      if (target) {
        go({ project: target.project.uid, window: target.win.uid, pane: slotRef(target.pane) });
        await post(`${paths.pane(target.project.uid, target.win.uid, target.pane.uid)}/focus`).catch(fail);
      }
      await post(paths.notificationAck(entry.id));
    } catch (err) {
      fail(err);
    } finally {
      busy.delete(entry.id);
    }
  }
</script>

<aside class="sidebar notify" aria-label="Notify" use:resizable={{ key: "notifyWidth", edge: "left" }}>
  <h2>
    <span>Notify</span>
    {#if live.notifications.length}
      <span class="count" class:crit={critical > 0}>{live.notifications.length}</span>
    {/if}
    <span class="grow"></span>
    <kbd>Alt-2</kbd>
  </h2>
  <ul class="list" bind:this={list} use:listKeys={onEscape}>
    {#each live.notifications as entry (entry.id)}
      <li>
        <button
          type="button"
          class="row notice-row {entry.severity || ''}"
          class:expired={expired(entry)}
          title={entry.id}
          onclick={() => select(entry)}
        >
          <span class="dot"></span>
          <div class="title">
            <span class="name">{(entry.text || entry.id).replace(/\s+/g, " ").trim()}</span>
          </div>
          <div class="meta">
            <span>{[entry.metadata?.agent || entry.source, entry.session].filter(Boolean).join(" · ")}</span>
            <span class="when">{expired(entry) ? t("web.notify.expired") : ago(entry.created_at)}</span>
          </div>
        </button>
      </li>
    {:else}
      <li class="empty">{t("web.notify.empty")}</li>
    {/each}
  </ul>
</aside>
