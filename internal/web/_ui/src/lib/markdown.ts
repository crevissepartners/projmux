// A deliberately small Markdown renderer: fenced code, headings, lists,
// blockquotes, tables, and inline code / bold / italic / links.
//
// It builds DOM nodes and never assigns HTML, so transcript text -- whatever
// the model and its tools produced -- cannot inject markup. Every anchor goes
// through one constructor that accepts http(s) only.
//
// References in running text become links too: a bare URL, and, when the
// agent's repository is known, `#1018`, a commit hash, and a path such as
// `internal/x.go:42`, each to GitHub at a revision the server pinned.

import { copyText } from "./clipboard";
import { t } from "./i18n.svelte";
import type { Repository } from "./types";

type Repo = Repository | null | false;

function el<K extends keyof HTMLElementTagNameMap>(tag: K, className = ""): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  if (className) node.className = className;
  return node;
}

function copyButton(text: string): HTMLButtonElement {
  const button = el("button", "copy-btn code-copy");
  button.type = "button";
  button.textContent = t("web.copy.label");
  let timer: ReturnType<typeof setTimeout> | undefined;
  button.addEventListener("click", async () => {
    const ok = await copyText(text);
    button.textContent = ok ? t("web.copy.done") : t("web.copy.failed");
    button.classList.toggle("done", ok);
    clearTimeout(timer);
    timer = setTimeout(() => {
      button.textContent = t("web.copy.label");
      button.classList.remove("done");
    }, 1500);
  });
  return button;
}

export function renderMarkdown(container: HTMLElement, text: string, repo: Repository | null = null): void {
  container.replaceChildren();
  const lines = String(text).split("\n");
  let list: { el: HTMLElement; ordered: boolean } | null = null;
  let quote: HTMLElement | null = null;
  const closeBlocks = () => {
    list = null;
    quote = null;
  };

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];

    const fence = /^\s*```(\w*)\s*$/.exec(line);
    if (fence) {
      closeBlocks();
      const body: string[] = [];
      for (i++; i < lines.length && !/^\s*```\s*$/.test(lines[i]); i++) body.push(lines[i]);
      const block = el("pre", "code");
      block.textContent = body.join("\n");
      if (fence[1]) block.dataset.lang = fence[1];
      const wrap = el("div", "code-wrap");
      wrap.append(block, copyButton(body.join("\n")));
      container.append(wrap);
      continue;
    }

    if (!line.trim()) {
      closeBlocks();
      continue;
    }

    // A table is a pipe row followed by a separator row. Checking the
    // separator keeps prose that starts with `|` from becoming a table.
    if (isTableRow(line) && i + 1 < lines.length && isTableRule(lines[i + 1])) {
      closeBlocks();
      const header = tableCells(line);
      const align = tableCells(lines[i + 1]).map((cell) => {
        const left = cell.startsWith(":");
        const right = cell.endsWith(":");
        return left && right ? "center" : right ? "right" : "";
      });
      const rows: string[][] = [];
      for (i += 2; i < lines.length && isTableRow(lines[i]); i++) rows.push(tableCells(lines[i]));
      i--;
      container.append(buildTable(header, rows, align, repo));
      continue;
    }

    const quoted = /^\s*>\s?(.*)$/.exec(line);
    if (quoted) {
      if (!quote) {
        quote = el("blockquote");
        container.append(quote);
      }
      const p = el("p");
      inline(p, quoted[1], repo);
      quote.append(p);
      continue;
    }
    quote = null;

    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      closeBlocks();
      const h = el("div", `md-h h${Math.min(heading[1].length, 4)}`);
      inline(h, heading[2], repo);
      container.append(h);
      continue;
    }

    if (/^\s*([-*_])\s*\1\s*\1[\s\-*_]*$/.test(line)) {
      closeBlocks();
      container.append(el("hr"));
      continue;
    }

    if (/^\s*([-*+]|\d+[.)])\s+/.test(line)) {
      const ordered = /^\s*\d+[.)]\s+/.test(line);
      if (!list || list.ordered !== ordered) {
        const node: HTMLElement = el(ordered ? "ol" : "ul", "md-list");
        container.append(node);
        list = { el: node, ordered };
      }
      const li = el("li");
      inline(li, line.replace(/^\s*([-*+]|\d+[.)])\s+/, ""), repo);
      list.el.append(li);
      continue;
    }
    list = null;

    const p = el("p");
    inline(p, line, repo);
    container.append(p);
  }
}

function isTableRow(line: string): boolean {
  return /^\s*\|.*\|\s*$/.test(line);
}

function isTableRule(line: string): boolean {
  return /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/.test(line);
}

/** A pipe inside inline code or escaped as `\|` is content, not a column break. */
function tableCells(line: string): string[] {
  const cells: string[] = [];
  let cell = "";
  let inCode = false;
  const body = line.trim().replace(/^\|/, "").replace(/\|$/, "");
  for (let i = 0; i < body.length; i++) {
    const ch = body[i];
    if (ch === "\\" && body[i + 1] === "|") {
      cell += "|";
      i++;
    } else if (ch === "`") {
      inCode = !inCode;
      cell += ch;
    } else if (ch === "|" && !inCode) {
      cells.push(cell.trim());
      cell = "";
    } else {
      cell += ch;
    }
  }
  cells.push(cell.trim());
  return cells;
}

function buildTable(header: string[], rows: string[][], align: string[], repo: Repo): HTMLElement {
  // Wide tables scroll inside their own box rather than widening the log.
  const wrap = el("div", "md-table");
  const table = el("table");
  const width = Math.max(header.length, ...rows.map((r) => r.length));
  const fill = (tr: HTMLElement, cells: string[], tag: "th" | "td") => {
    for (let c = 0; c < width; c++) {
      const cell = el(tag);
      if (align[c]) cell.style.textAlign = align[c];
      inline(cell, cells[c] ?? "", repo);
      tr.append(cell);
    }
  };
  const thead = el("thead");
  const headRow = el("tr");
  fill(headRow, header, "th");
  thead.append(headRow);
  const tbody = el("tbody");
  for (const row of rows) {
    const tr = el("tr");
    fill(tr, row, "td");
    tbody.append(tr);
  }
  table.append(thead, tbody);
  wrap.append(table);
  return wrap;
}

/**
 * Inline spans. `repo` false means no links at all, which is how an anchor's
 * own label is rendered: an anchor inside an anchor is not clickable.
 */
function inline(parent: HTMLElement, text: string, repo: Repo): void {
  const pattern = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\*[^*\n]+\*)|(\[[^\]]+\]\([^)\s]+\))/g;
  let last = 0;
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(text)) !== null) {
    if (match.index > last) linkText(parent, text.slice(last, match.index), repo);
    const token = match[0];
    if (token.startsWith("`")) {
      const code = el("code");
      code.textContent = token.slice(1, -1);
      // Agents put paths and hashes in code spans more often than not, so a
      // span that is exactly one reference links as a whole.
      const href = repo === false ? null : referenceHref(token.slice(1, -1).trim(), repo);
      parent.append(href ? anchor(href, code) : code);
    } else if (token.startsWith("**")) {
      const strong = el("strong");
      inline(strong, token.slice(2, -2), repo);
      parent.append(strong);
    } else if (token.startsWith("[")) {
      const split = token.indexOf("](");
      const label = token.slice(1, split);
      const target = token.slice(split + 2, -1);
      const href = repo === false ? null : /^https?:\/\//i.test(target) ? target : pathHref(target, repo, true);
      if (href) {
        const a = anchor(href);
        inline(a, label, false);
        parent.append(a);
      } else {
        parent.append(token);
      }
    } else {
      const em = el("em");
      inline(em, token.slice(1, -1), repo);
      parent.append(em);
    }
    last = pattern.lastIndex;
  }
  if (last < text.length) linkText(parent, text.slice(last), repo);
}

/** The one place an anchor is made: http(s) only, new tab, no opener. */
function anchor(href: string, child?: Node): HTMLAnchorElement {
  if (!/^https?:\/\//i.test(href)) throw new Error(`refusing href ${href}`);
  const a = el("a");
  a.href = href;
  a.target = "_blank";
  a.rel = "noopener noreferrer";
  if (child) a.append(child);
  return a;
}

// A bare URL stops at whitespace, quotes, angle brackets and any non-ASCII
// character -- Korean text often follows a URL with no space. Paths need a
// slash or a line number and a hash needs a letter and a digit, so ordinary
// words and numbers stay words and numbers.
const refPattern = new RegExp(
  [
    String.raw`(?<url>https?:\/\/[^\s<>"'\u0060\u0080-\uffff]+)`,
    String.raw`(?<![\w/.:@#~-])(?<path>(?:\.{0,2}\/)?[\w.@+-]+(?:\/[\w.@+-]+)*\/?(?::\d+(?:-\d+)?)?)`,
    String.raw`(?<![\w&/#-])#(?<issue>\d{1,7})(?![\w-])`,
    String.raw`(?<![\w/.-])(?<sha>[0-9a-f]{7,40})(?![\w-])`,
  ].join("|"),
  "g",
);

function linkText(parent: HTMLElement, text: string, repo: Repo): void {
  if (repo === false) {
    parent.append(text);
    return;
  }
  let last = 0;
  for (const m of text.matchAll(refPattern)) {
    let token = m[0];
    let href: string | null;
    if (m.groups?.url) {
      token = trimUrl(token);
      href = token;
    } else {
      href = referenceHref(token, repo);
    }
    if (!href) continue;
    const index = m.index ?? 0;
    if (index > last) parent.append(text.slice(last, index));
    parent.append(anchor(href, document.createTextNode(token)));
    last = index + token.length;
  }
  if (last < text.length) parent.append(text.slice(last));
}

/** Sentence punctuation is not part of a URL, nor a bracket it did not open. */
export function trimUrl(url: string): string {
  for (;;) {
    const end = url.at(-1) || "";
    if (/[.,;:!?'"*]/.test(end)) url = url.slice(0, -1);
    else if (end === ")" && count(url, "(") < count(url, ")")) url = url.slice(0, -1);
    else if (end === "]" && count(url, "[") < count(url, "]")) url = url.slice(0, -1);
    else return url;
  }
}

function count(text: string, ch: string): number {
  return text.split(ch).length - 1;
}

/** A whole token that is one reference, as a URL, or null. */
export function referenceHref(token: string, repo: Repository | null): string | null {
  if (/^https?:\/\/\S+$/i.test(token)) return trimUrl(token) === token ? token : null;
  if (!repo) return null;
  const issue = /^#(\d{1,7})$/.exec(token);
  // /issues/N redirects to /pull/N when N is a pull request.
  if (issue) return `${repo.web}/issues/${issue[1]}`;
  if (/^[0-9a-f]{7,40}$/.test(token) && /[a-f]/.test(token) && /\d/.test(token)) {
    return `${repo.web}/commit/${token}`;
  }
  return pathHref(token, repo);
}

/**
 * A file or directory in the agent's checkout, as a GitHub URL pinned to the
 * server's revision, or null. An absolute path has to be under the checkout
 * root; a relative one is taken from the root and has to look like a path. An
 * `explicit` target, the one in `[text](target)`, only has to be a path.
 */
export function pathHref(token: string, repo: Repository | null, explicit = false): string | null {
  if (!repo) return null;
  const m = /^(.+?)(?::(\d+)(?:-(\d+))?)?$/.exec(token);
  if (!m) return null;
  let path = m[1];
  const from = m[2];
  const to = m[3];
  // A scheme, a query or a fragment is not a file name.
  if (/[:?#]/.test(path)) return null;
  if (path.startsWith("/")) {
    if (!path.startsWith(`${repo.root}/`)) return null;
    path = path.slice(repo.root.length + 1);
  } else if (path.startsWith("./")) {
    path = path.slice(2);
  }
  const dir = path.endsWith("/");
  const parts = path.replace(/\/$/, "").split("/");
  if (!path || parts.some((p) => p === "" || p === "." || p === "..")) return null;
  // Another worktree's copy and git's own files are not this revision's.
  if (parts[0] === ".git" || parts[0] === ".wt") return null;
  if (dir && from) return null;
  if (!explicit) {
    const named = /\.[A-Za-z][A-Za-z0-9]{0,9}$/.test(parts.at(-1) || "");
    if (!dir && !named) return null;
    if (!dir && parts.length < 2 && !from && !token.startsWith("/")) return null;
  }
  const encoded = parts.map(encodeURIComponent).join("/");
  if (dir) return `${repo.web}/tree/${repo.rev}/${encoded}`;
  let href = `${repo.web}/blob/${repo.rev}/${encoded}`;
  if (from) {
    // GitHub renders Markdown unless asked for the source, and a rendered
    // page has no line anchors.
    if (/\.(md|markdown|mdx)$/i.test(path)) href += "?plain=1";
    href += `#L${from}${to ? `-L${to}` : ""}`;
  }
  return href;
}

/** Svelte action: `<div use:markdown={{ text, repo }}>`. */
export function markdown(node: HTMLElement, params: { text: string; repo: Repository | null }) {
  renderMarkdown(node, params.text, params.repo);
  return {
    update(next: { text: string; repo: Repository | null }) {
      renderMarkdown(node, next.text, next.repo);
    },
  };
}
