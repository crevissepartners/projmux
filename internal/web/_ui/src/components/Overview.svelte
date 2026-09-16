<script lang="ts">
  // Nothing chosen, or a Project without a Window: the page shows what is
  // running instead of a prompt to go pick something. The question on
  // arrival is "what needs me", and the answer is already in the tree.
  import { activityOf, byAttention, needsYou } from "../lib/activity";
  import { providerText } from "../lib/errors";
  import { t } from "../lib/i18n.svelte";
  import { go } from "../lib/router.svelte";
  import { live } from "../lib/state.svelte";
  import { ago } from "../lib/time";
  import { livePanes, paneLabel, slotRef, type ProjectView } from "../lib/tree";

  let { project }: { project: ProjectView | null } = $props();

  const rows = $derived(livePanes(live.tree, project).sort(byAttention));
  const waiting = $derived(rows.filter((r) => needsYou(activityOf(r.pane)?.tone)).length);
  const busy = $derived(rows.filter((r) => activityOf(r.pane)?.tone === "busy").length);
</script>

<div class="overview">
  <div class="overview-head">
    <h3>{project ? project.name : t("web.overview.everything")}</h3>
    <span class="overview-sum">{t("web.overview.summary", { agents: rows.length, busy, waiting })}</span>
  </div>
  {#if !rows.length}
    <p class="empty">{project ? t("web.overview.empty_project") : t("web.overview.empty")}</p>
  {:else}
    <div class="cards">
      {#each rows as row (row.pane.uid)}
        {@const state = activityOf(row.pane)}
        <button
          type="button"
          class="card"
          data-provider={row.pane.agent?.provider}
          data-activity={state?.tone || ""}
          onclick={() => go({ project: row.project.uid, window: row.win.uid, pane: slotRef(row.pane) })}
        >
          <div class="card-top">
            <span class="slot-dot"></span>
            <span class="card-name">{paneLabel(row.pane).name}</span>
            <span class="slot-activity {state?.tone || ''}">{state ? t(`web.activity.${state.kind}`) : ""}</span>
          </div>
          <div class="card-where">{row.project.name} / {row.win.name}</div>
          <div class="card-meta">
            {[providerText(row.pane.agent?.provider || ""), ago(row.pane.agent?.activityAt)].filter(Boolean).join(" · ")}
          </div>
        </button>
      {/each}
    </div>
  {/if}
</div>
