// registry-ui behaviour. Loaded from a file, never inline, so the CSP can stay
// at default-src 'self'. Listeners sit on document, so content swapped in by
// htmx works without rebinding.
(() => {
  const root = document.documentElement;

  // The theme toggle. The server reads the cookie and renders the class on
  // <html>, so the next page load doesn't flash.
  const isDark = () =>
    root.classList.contains("dark") ||
    (!root.classList.contains("light") && matchMedia("(prefers-color-scheme: dark)").matches);

  // The new theme spreads out from the button as a growing circle, where the
  // browser has view transitions and the user hasn't asked for less motion.
  document.addEventListener("click", (e) => {
    const btn = e.target.closest("[data-theme-toggle]");
    if (!btn) return;
    const theme = isDark() ? "light" : "dark";
    const apply = () => {
      root.classList.remove("light", "dark");
      root.classList.add(theme);
    };
    document.cookie = `theme=${theme}; path=/; max-age=31536000; samesite=lax`;
    if (!document.startViewTransition || matchMedia("(prefers-reduced-motion: reduce)").matches) return apply();
    const b = btn.getBoundingClientRect();
    const x = b.left + b.width / 2;
    const y = b.top + b.height / 2;
    const r = Math.hypot(Math.max(x, innerWidth - x), Math.max(y, innerHeight - y));
    document.startViewTransition(apply).ready.then(() =>
      root.animate(
        { clipPath: [`circle(0px at ${x}px ${y}px)`, `circle(${r}px at ${x}px ${y}px)`] },
        { duration: 500, easing: "cubic-bezier(0.2, 0, 0, 1)", pseudoElement: "::view-transition-new(root)" },
      ),
    );
  });

  // Copy buttons ([data-copy], the "copy" partial). navigator.clipboard only
  // exists on HTTPS and localhost, so plain-HTTP deployments fall back to
  // execCommand on a hidden textarea.
  const copyText = async (text) => {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      const focused = document.activeElement;
      const area = document.createElement("textarea");
      area.value = text;
      area.readOnly = true;
      area.className = "fixed top-0 left-0 opacity-0";
      document.body.append(area);
      area.select();
      try {
        return document.execCommand("copy");
      } catch {
        return false;
      } finally {
        area.remove();
        focused?.focus();
      }
    }
  };

  document.addEventListener("click", async (e) => {
    const btn = e.target.closest("[data-copy]");
    if (!btn) return;
    const ok = await copyText(btn.dataset.copy);
    const label = btn.querySelector("[data-copy-label]");
    if (label) label.dataset.caption ??= label.textContent; // "Copy", "Copy digest"…
    btn.toggleAttribute("data-copied", ok);
    if (label) label.textContent = ok ? "Copied" : "Copy failed";
    clearTimeout(btn.copyTimer);
    btn.copyTimer = setTimeout(() => {
      btn.removeAttribute("data-copied");
      if (label) label.textContent = label.dataset.caption;
    }, 2000);
  });

  // The tag page's ?platform= link: bring the highlighted card into view.
  document.querySelector('article[aria-current="true"]')?.scrollIntoView({ block: "center" });

  // Dates (<time datetime>, format.Time) say on hover when exactly. The
  // server wrote that in UTC; here it becomes the viewer's own zone and
  // locale, "7 Nov 2026, 10:30:00 GMT+11". data-title is a sentence with {}
  // where the time goes.
  const localTime = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "long" });
  const localTimes = () => {
    for (const el of document.querySelectorAll("time[datetime]")) {
      const at = new Date(el.dateTime);
      if (isNaN(at)) continue;
      const text = localTime.format(at);
      el.title = el.dataset.title ? el.dataset.title.replace("{}", text) : text;
    }
  };
  document.addEventListener("htmx:afterSettle", localTimes);
  localTimes();

  // "/" focuses the header search ([data-hotkey="/"]) on every page. Esc in
  // any search field clears it, then leaves it; the input event lets an htmx
  // filter catch up.
  document.addEventListener("keydown", (e) => {
    const t = e.target;
    if (e.key === "Escape" && t.matches?.('input[type="search"]')) {
      e.preventDefault();
      if (t.value) {
        t.value = "";
        t.dispatchEvent(new Event("input", { bubbles: true }));
      } else {
        t.blur();
      }
      return;
    }
    if (e.key !== "/" || e.ctrlKey || e.metaKey || e.altKey || e.defaultPrevented) return;
    if (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName)) return;
    const field = document.querySelector('[data-hotkey="/"]');
    if (!field) return;
    e.preventDefault();
    field.focus();
    field.select();
  });

  // The catalog's Tags figure stays hidden until every row of the unfiltered
  // list has loaded its tag count (rows load as they scroll into view), then
  // shows their sum. Sorted by tag count or date, the rows arrive loaded.
  const tagTotal = () => {
    const list = document.querySelector("[data-repo-list]");
    const out = document.querySelector('[data-stat="tags"]');
    if (!list || !out || list.querySelector("[data-row]:not([data-tags])")) return;
    let total = 0;
    for (const row of list.querySelectorAll("[data-tags]")) total += Number(row.dataset.tags);
    out.textContent = total;
    out.closest("[hidden]")?.removeAttribute("hidden");
  };
  document.addEventListener("htmx:afterSettle", tagTotal);
  tagTotal();

  // Menus (<details data-menu>, the "sort-menu" partial) close on Escape,
  // handing focus back to their button, or on a click outside them.
  document.addEventListener("click", (e) => {
    for (const menu of document.querySelectorAll("details[data-menu][open]")) {
      if (!menu.contains(e.target)) menu.open = false;
    }
  });
  document.addEventListener("keydown", (e) => {
    const menu = e.key === "Escape" && e.target.closest?.("details[data-menu][open]");
    if (!menu) return;
    menu.open = false;
    menu.querySelector("summary").focus();
  });

  // The Cards/List toggle remembers the choice in a cookie, which the server
  // reads when the URL has no view, and keeps the filter that htmx has been
  // writing into the URL.
  document.addEventListener("click", (e) => {
    const btn = e.target.closest('button[name="view"]');
    if (!btn) return;
    e.preventDefault();
    document.cookie = `view=${btn.value}; path=/; max-age=31536000; samesite=lax`;
    const url = new URL(location.href);
    url.searchParams.set("view", btn.value);
    location.assign(url);
  });

  // List-view selection. The selected tags and their commands live in
  // sessionStorage, so they survive filtering and paging; leaving the
  // repository (anywhere but its tabs and tag pages) drops them.
  const storeKey = "selection";
  let sel = { path: "", tags: new Map() };
  try {
    const saved = JSON.parse(sessionStorage.getItem(storeKey));
    // An older version saved a bare command per tag: drop that selection.
    if (saved?.tags.every(([, v]) => v && typeof v === "object")) sel = { path: saved.path, tags: new Map(saved.tags) };
  } catch {}
  const save = () => {
    try {
      sessionStorage.setItem(storeKey, JSON.stringify({ path: sel.path, tags: [...sel.tags] }));
    } catch {}
  };
  const here = location.pathname;
  if (sel.path && here !== sel.path && !here.startsWith(sel.path + ":") && !here.startsWith(sel.path + "@")) {
    sel = { path: "", tags: new Map() };
    save();
  }

  let anchor = null; // the tag last clicked, where a Shift-click range starts
  let announced = 0;
  const rows = () => [...document.querySelectorAll("tr[data-tag]")];
  const copies = (row) => ({ pull: row.dataset.pull, ref: row.dataset.ref }); // what a selected row copies

  // sync draws the selection: checkboxes, select-all, the bar and its count.
  const sync = () => {
    const box = document.querySelector("[data-select]");
    if (!box) return;
    if (sel.path !== box.dataset.select) sel = { path: box.dataset.select, tags: new Map() };
    const shown = rows();
    let n = 0;
    for (const row of shown) {
      const on = sel.tags.has(row.dataset.tag);
      row.querySelector("input").checked = on;
      if (!on) continue;
      n++;
      // A loaded row knows its exact command; an unloaded one only guessed.
      if (!row.hasAttribute("aria-busy")) sel.tags.set(row.dataset.tag, copies(row));
    }
    const all = document.querySelector("[data-select-all]");
    if (all) {
      all.checked = n > 0 && n === shown.length;
      all.indeterminate = n > 0 && n < shown.length;
    }
    const count = sel.tags.size;
    box.querySelector("[data-selection-bar]").hidden = !count;
    box.querySelector("[data-selection-count]").textContent = count;
    if (count !== announced) {
      box.querySelector("[data-selection-status]").textContent = count ? `${count} selected` : "Selection cleared";
      announced = count;
    }
    save();
  };
  sync();
  document.addEventListener("htmx:afterSettle", sync);

  const toast = (text) => {
    const t = document.querySelector("[data-toast]");
    if (!t) return;
    t.querySelector("span").textContent = text;
    t.hidden = false;
    clearTimeout(t.timer);
    t.timer = setTimeout(() => (t.hidden = true), 2500);
  };
  const plural = (n, unit) => `${n} ${unit}${n === 1 ? "" : "s"}`;

  document.addEventListener("click", async (e) => {
    const all = e.target.closest("[data-select-all]");
    if (all) {
      for (const row of rows()) {
        if (all.checked) sel.tags.set(row.dataset.tag, copies(row));
        else sel.tags.delete(row.dataset.tag);
      }
      sync();
      return;
    }

    const action = e.target.closest("[data-select] [data-action]")?.dataset.action;
    if (action === "clear") {
      sel.tags.clear();
      sync();
      document.querySelector("[data-select-all]")?.focus();
    } else if (action) {
      const [unit, pick] = { names: ["tag name", ([tag]) => tag], refs: ["reference", ([, v]) => v.ref], pulls: ["pull command", ([, v]) => v.pull] }[action];
      const lines = [...sel.tags].map(pick).filter(Boolean);
      const ok = await copyText(lines.join("\n"));
      toast(ok ? `Copied ${plural(lines.length, unit)}` : "Copy failed");
    }

    // A click on a row, or on its checkbox, outside its links and buttons.
    const row = e.target.closest("tr[data-tag]");
    if (!row || e.target.closest("a, button")) return;
    const input = row.querySelector("input");
    const on = e.target === input ? input.checked : !input.checked; // the browser has already toggled a clicked checkbox
    const shown = rows();
    const from = shown.findIndex((r) => r.dataset.tag === anchor);
    const to = shown.indexOf(row);
    const range = e.shiftKey && from >= 0 ? shown.slice(Math.min(from, to), Math.max(from, to) + 1) : [row];
    for (const r of range) {
      if (on) sel.tags.set(r.dataset.tag, copies(r));
      else sel.tags.delete(r.dataset.tag);
    }
    anchor = row.dataset.tag;
    if (e.shiftKey) getSelection()?.removeAllRanges();
    sync();
  });
})();
