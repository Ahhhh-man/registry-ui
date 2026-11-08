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

  // "/" focuses the last [data-hotkey="/"] field, so a page's own filter wins
  // over the header search. Esc leaves it.
  document.addEventListener("keydown", (e) => {
    const t = e.target;
    if (e.key === "Escape" && t.matches?.('[data-hotkey="/"]')) {
      t.blur();
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
})();
