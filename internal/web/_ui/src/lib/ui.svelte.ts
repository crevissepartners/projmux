// Page-local view state: what is open, and the focus request the composer
// honours. None of it is shared; the toggles persist per browser.

import { load, save } from "./local";

export const ui = $state({
  sidebar: load("sidebar", true),
  notify: load("notify", false),
  showShell: load("shell", false),
  layout: load("layout", false),
  desktopNotify: load("desktopNotify", false),
  overlay: "" as "" | "launch" | "launch-advanced" | "resume" | "settings" | "switcher" | "help",
  /** Bumped after a settings save, so what reads settings reads them again. */
  settingsVersion: 0,
  /** The Project a window is being created in, while the create runs. */
  creatingWindow: "",
  /** A pane whose composer should take the caret once it exists. */
  focusComposer: "",
});

export function setToggle(key: "sidebar" | "notify" | "showShell" | "layout" | "desktopNotify", value: boolean) {
  ui[key] = value;
  save(key === "showShell" ? "shell" : key, value);
}
