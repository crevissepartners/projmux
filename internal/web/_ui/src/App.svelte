<script lang="ts">
  import { activityOf, needsYou } from "./lib/activity";
  import { focusList } from "./lib/actions";
  import { createWindow } from "./lib/commands";
  import { stopGeometry, followGeometry, geometry } from "./lib/geometry.svelte";
  import { loadMessages, t } from "./lib/i18n.svelte";
  import { canonicalize, go, route } from "./lib/router.svelte";
  import { connect, live, refresh } from "./lib/state.svelte";
  import { toast } from "./lib/toast.svelte";
  import { findProject, findSlot, findWindow, livePanes, locateSlot, paneLabel, slotRef, type PaneView } from "./lib/tree";
  import { setToggle, ui } from "./lib/ui.svelte";
  import Help from "./components/Help.svelte";
  import LayoutPreview from "./components/LayoutPreview.svelte";
  import NotifySidebar from "./components/NotifySidebar.svelte";
  import Overview from "./components/Overview.svelte";
  import PaneSplit from "./components/PaneSplit.svelte";
  import ProjectSidebar from "./components/ProjectSidebar.svelte";
  import LaunchPicker from "./components/LaunchPicker.svelte";
  import ResumePicker from "./components/ResumePicker.svelte";
  import Settings from "./components/Settings.svelte";
  import StatusBar from "./components/StatusBar.svelte";
  import Switcher from "./components/Switcher.svelte";
  import Toasts from "./components/Toasts.svelte";
  import WindowBar from "./components/WindowBar.svelte";
  import WindowRecord from "./components/WindowRecord.svelte";

  loadMessages();
  connect();

  const project = $derived(findProject(live.tree, route.sel.project));
  const win = $derived(findWindow(project, route.sel.window));
  const pane = $derived(findSlot(win, route.sel.pane));

  // An address written before agents were addressed by their own uid names a
  // pane an agent holds; it is rewritten to the agent's address in place.
  $effect(() => {
    if (route.legacy) canonicalize(route.sel);
    if (!project || !win || !pane) return;
    const canonical = slotRef(pane);
    if (canonical !== route.sel.pane) canonicalize({ project: project.uid, window: win.uid, pane: canonical });
  });

  // A short `/a/{agent}` link becomes the agent's full address once the graph
  // says where it is; an agent the graph does not have leads to the overview.
  $effect(() => {
    const agent = route.short;
    if (!agent || !live.updatedAt) return;
    const found = locateSlot(live.tree, agent);
    canonicalize(
      found
        ? { project: found.project.uid, window: found.win.uid, pane: slotRef(found.pane) }
        : { project: null, window: null, pane: null },
    );
  });

  // Shell panes hold a place in the real window but carry nothing this page
  // shows, so they are left out unless asked for.
  const drawn = $derived((win?.panes || []).filter((p) => p.runtimeId && (ui.showShell || p.agent)));

  $effect(() => {
    if (win && drawn.length) followGeometry(win.uid);
    else stopGeometry();
  });

  // Focusing a pane puts the caret in its composer, unless the focus came
  // from a click on something inside that slot that took focus itself.
  let lastFocused: string | null = null;
  $effect(() => {
    const uid = pane?.uid ?? null;
    if (uid === lastFocused) return;
    lastFocused = uid;
    if (!uid) return;
    const slot = document.querySelector(`.slot[data-pane="${uid}"]`);
    if (slot?.contains(document.activeElement)) return;
    ui.focusComposer = uid;
  });

  // The page title carries the waiting count, visible from any other tab.
  $effect(() => {
    const waiting = livePanes(live.tree).filter((r) => needsYou(activityOf(r.pane)?.tone)).length;
    document.title = waiting ? `(${waiting}) projmux` : "projmux";
  });

  // Desktop notifications, off until asked for: when an agent starts waiting
  // or finishes while this tab is in the background. The first read only
  // records state, so opening the page does not announce everything.
  const lastTone = new Map<string, string>();
  $effect(() => {
    const seen = new Set<string>();
    for (const { project: p, win: w, pane: row } of livePanes(live.tree)) {
      const agent = row.agent!;
      seen.add(agent.uid);
      const state = activityOf(row);
      const tone = state?.tone || "";
      const before = lastTone.get(agent.uid);
      lastTone.set(agent.uid, tone);
      if (before === undefined || before === tone || !state) continue;
      if (!["wait", "alert", "done"].includes(tone)) continue;
      if (!ui.desktopNotify || !document.hidden || typeof Notification === "undefined") continue;
      if (Notification.permission !== "granted") continue;
      const note = new Notification(`${paneLabel(row).name}: ${t(`web.activity.${state.kind}`)}`, {
        body: `${p.name} / ${w.name}`,
        tag: agent.uid,
      });
      note.onclick = () => {
        window.focus();
        go({ project: p.uid, window: w.uid, pane: slotRef(row) });
        note.close();
      };
    }
    for (const uid of [...lastTone.keys()]) if (!seen.has(uid)) lastTone.delete(uid);
  });

  async function toggleDesktopNotify() {
    if (typeof Notification === "undefined") {
      toast(t("web.desktop.unsupported"), "err");
      return;
    }
    if (!ui.desktopNotify && Notification.permission !== "granted") {
      if ((await Notification.requestPermission()) !== "granted") {
        toast(t("web.desktop.denied"), "err");
        return;
      }
    }
    setToggle("desktopNotify", !ui.desktopNotify);
    toast(ui.desktopNotify ? t("web.desktop.on") : t("web.desktop.off"));
  }

  let projectList: HTMLElement | undefined = $state();
  let notifyList: HTMLElement | undefined = $state();

  function closeSidebar(which: "sidebar" | "notify") {
    setToggle(which, false);
    if (pane) ui.focusComposer = pane.uid;
  }

  /**
   * The pane in that direction by the real layout: among panes fully on that
   * side, the nearest edge wins and the nearest centre breaks the tie, which
   * is how tmux select-pane reads to a person. Without geometry it falls back
   * to the drawn order.
   */
  function selectPane(direction: "left" | "right" | "up" | "down") {
    if (!project || !win || drawn.length < 2) return;
    const at = drawn.findIndex((p) => p.uid === pane?.uid);
    const current = drawn[at] || drawn[0];
    const to = (node: PaneView | null) => node && go({ project: project.uid, window: win.uid, pane: slotRef(node) });
    const here = geometry.panes[current.runtimeId];
    if (!here) {
      const step = direction === "right" || direction === "down" ? 1 : -1;
      to(drawn[((at < 0 ? 0 : at + step) + drawn.length) % drawn.length]);
      return;
    }
    const horizontal = direction === "left" || direction === "right";
    const centre = (b: { x: number; y: number; width: number; height: number }) =>
      horizontal ? b.y + b.height / 2 : b.x + b.width / 2;
    let best: PaneView | null = null;
    let bestKey: [number, number] | null = null;
    for (const node of drawn) {
      if (node.uid === current.uid) continue;
      const box = geometry.panes[node.runtimeId];
      if (!box) continue;
      let edge: number;
      if (direction === "left") {
        if (box.x + box.width > here.x) continue;
        edge = here.x - (box.x + box.width);
      } else if (direction === "right") {
        if (box.x < here.x + here.width) continue;
        edge = box.x - (here.x + here.width);
      } else if (direction === "up") {
        if (box.y + box.height > here.y) continue;
        edge = here.y - (box.y + box.height);
      } else {
        if (box.y < here.y + here.height) continue;
        edge = box.y - (here.y + here.height);
      }
      const key: [number, number] = [edge, Math.abs(centre(box) - centre(here))];
      if (!bestKey || key[0] < bestKey[0] || (key[0] === bestKey[0] && key[1] < bestKey[1])) {
        best = node;
        bestKey = key;
      }
    }
    to(best);
  }

  function stepWindow(step: number) {
    const windows = project?.windows || [];
    if (!project || windows.length < 2) return;
    const at = windows.findIndex((w) => w.uid === route.sel.window);
    const next = windows[((at < 0 ? 0 : at + step) + windows.length) % windows.length];
    // Land on a pane: arriving with nothing focused is not what changing
    // Window means.
    const first = next.panes.find((p) => p.runtimeId && (ui.showShell || p.agent));
    go({ project: project.uid, window: next.uid, pane: first ? slotRef(first) : null });
  }

  const arrows: Record<string, "left" | "right" | "up" | "down"> = {
    ArrowLeft: "left",
    ArrowRight: "right",
    ArrowUp: "up",
    ArrowDown: "down",
  };

  const inTextField = () => ["INPUT", "TEXTAREA", "SELECT"].includes(document.activeElement?.tagName || "");

  function keydown(event: KeyboardEvent) {
    // Alt-1 and Alt-2 open their sidebar and put the keyboard in it; pressed
    // again while focus is there, they close it.
    if (event.altKey && event.key === "1") {
      event.preventDefault();
      if (ui.sidebar && projectList?.contains(document.activeElement)) closeSidebar("sidebar");
      else {
        setToggle("sidebar", true);
        focusList(projectList);
      }
      return;
    }
    if (event.altKey && event.key === "2") {
      event.preventDefault();
      if (ui.notify && notifyList?.contains(document.activeElement)) closeSidebar("notify");
      else {
        setToggle("notify", true);
        focusList(notifyList);
      }
      return;
    }
    // New window. Alt-N is listed beside Ctrl-N because browsers keep Ctrl-N
    // for themselves and do not always hand it over.
    const newWindow =
      !inTextField() &&
      !event.metaKey &&
      event.key.toLowerCase() === "n" &&
      ((event.ctrlKey && !event.altKey) || (event.altKey && !event.ctrlKey));
    if (newWindow) {
      if (!route.sel.project) return;
      event.preventDefault();
      createWindow(route.sel.project);
      return;
    }
    // Alt-arrow between panes, Alt-Shift-arrow between windows: the pairing
    // projmux's generated tmux config uses.
    if (event.altKey && !event.ctrlKey && !event.metaKey && arrows[event.key]) {
      event.preventDefault();
      if (event.shiftKey) stepWindow(event.key === "ArrowRight" ? 1 : -1);
      else selectPane(arrows[event.key]);
      return;
    }
    if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === "k") {
      event.preventDefault();
      ui.overlay = ui.overlay === "switcher" ? "" : "switcher";
      return;
    }
    // Alt-7 opens the launcher and Alt-4 the resume picker, as in the
    // terminal; pressed again, each closes.
    if (event.altKey && event.key === "7") {
      event.preventDefault();
      ui.overlay = ui.overlay === "launch" ? "" : "launch";
      return;
    }
    if (event.altKey && event.key === "5") {
      event.preventDefault();
      ui.overlay = ui.overlay === "settings" ? "" : "settings";
      return;
    }
    if (event.altKey && event.key === "4") {
      event.preventDefault();
      ui.overlay = ui.overlay === "resume" ? "" : "resume";
      return;
    }
    if (event.key === "Escape" && ui.overlay) {
      event.preventDefault();
      ui.overlay = "";
      return;
    }
    if (event.metaKey || event.ctrlKey || event.altKey || inTextField()) return;
    if (event.key === "?") {
      event.preventDefault();
      ui.overlay = ui.overlay === "help" ? "" : "help";
      return;
    }
    if (event.key === "r") refresh();
  }

  const closeOverlay = () => (ui.overlay = "");
  // The badge names the Project, as the terminal status bar does; the tmux
  // session name is an internal handle and only fills in for a missing name.
  const barSession = $derived(project?.name || project?.sessionName || "");
  const barPath = $derived(pane?.cwd || project?.root || "");
</script>

<svelte:window onkeydown={keydown} />

<div class="app">
  {#if ui.sidebar}
    <ProjectSidebar bind:list={projectList} onEscape={() => closeSidebar("sidebar")} />
  {/if}

  <main class="main">
    <WindowBar {project} current={win} onDesktopNotify={toggleDesktopNotify} />
    {#if project && win && drawn.length}
      <PaneSplit {project} {win} panes={drawn} focused={pane?.uid ?? null} />
    {:else if win}
      <div class="content" id="detail"><WindowRecord {win} /></div>
    {:else}
      <div class="content" id="detail"><Overview {project} /></div>
    {/if}
  </main>

  {#if ui.notify}
    <NotifySidebar bind:list={notifyList} onEscape={() => closeSidebar("notify")} />
  {/if}
</div>

<StatusBar
  session={barSession}
  path={barPath}
  paneUID={pane?.uid || ""}
  onNotify={() => setToggle("notify", !ui.notify)}
  onSession={() => {
    setToggle("sidebar", true);
    focusList(projectList);
  }}
/>

{#if ui.layout && win?.runtimeId}
  <LayoutPreview window={win.uid} ownRuntime={pane?.runtimeId || ""} onClose={() => setToggle("layout", false)} />
{/if}
{#if ui.overlay === "launch" || ui.overlay === "launch-advanced"}
  <LaunchPicker advanced={ui.overlay === "launch-advanced"} onClose={closeOverlay} />
{:else if ui.overlay === "settings"}
  <Settings onClose={closeOverlay} />
{:else if ui.overlay === "resume"}
  <ResumePicker onClose={closeOverlay} />
{:else if ui.overlay === "switcher"}
  <Switcher onClose={closeOverlay} />
{:else if ui.overlay === "help"}
  <Help onClose={closeOverlay} />
{/if}
<Toasts />
