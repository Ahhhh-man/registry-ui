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

  document.addEventListener("click", (e) => {
    if (!e.target.closest("[data-theme-toggle]")) return;
    const theme = isDark() ? "light" : "dark";
    root.classList.remove("light", "dark");
    root.classList.add(theme);
    document.cookie = `theme=${theme}; path=/; max-age=31536000; samesite=lax`;
  });

  // Copy buttons ([data-copy], the "copy" partial). navigator.clipboard only
  // exists on HTTPS and localhost, so plain-HTTP deployments fall back to
  // execCommand on a hidden textarea.
  const fallbackCopy = (text) => {
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
    }
  };

  document.addEventListener("click", async (e) => {
    const btn = e.target.closest("[data-copy]");
    if (!btn) return;
    let ok;
    try {
      await navigator.clipboard.writeText(btn.dataset.copy);
      ok = true;
    } catch {
      ok = fallbackCopy(btn.dataset.copy);
      btn.focus();
    }
    const label = btn.querySelector("[data-copy-label]");
    btn.toggleAttribute("data-copied", ok);
    if (label) label.textContent = ok ? "Copied" : "Copy failed";
    clearTimeout(btn.copyTimer);
    btn.copyTimer = setTimeout(() => {
      btn.removeAttribute("data-copied");
      if (label) label.textContent = "Copy";
    }, 2000);
  });

  // "/" focuses the last [data-hotkey="/"] field, so a page's own filter wins
  // over the header search. Esc clears it, then leaves it; the input event
  // lets an htmx filter catch up.
  document.addEventListener("keydown", (e) => {
    const t = e.target;
    if (e.key === "Escape" && t.matches?.('[data-hotkey="/"]')) {
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
    const fields = document.querySelectorAll('[data-hotkey="/"]');
    const field = fields[fields.length - 1];
    if (!field) return;
    e.preventDefault();
    field.focus();
    field.select();
  });

  // The catalog's Tags figure stays hidden until every row of the unfiltered
  // list has loaded its tag count (rows load as they scroll into view), then
  // shows their sum.
  document.addEventListener("htmx:afterSettle", () => {
    const list = document.querySelector("[data-repo-list]");
    const out = document.querySelector('[data-stat="tags"]');
    if (!list || !out || list.querySelector("[data-row]:not([data-tags])")) return;
    let total = 0;
    for (const row of list.querySelectorAll("[data-tags]")) total += Number(row.dataset.tags);
    out.textContent = total;
    out.closest("[hidden]")?.removeAttribute("hidden");
  });
})();
