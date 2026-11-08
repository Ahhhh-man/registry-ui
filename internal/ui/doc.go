// Package ui embeds the templates and static assets.
//
// static/htmx.min.js is htmx 2.0.11 (0BSD licence), unmodified from
// https://unpkg.com/htmx.org@2.0.11/dist/htmx.min.js,
// sha256 d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717.
//
// The fonts are Google Fonts' Latin subsets, unmodified, both under the SIL
// Open Font License 1.1 (static/fonts/OFL-*.txt):
//   - static/fonts/inter.woff2 is Inter 4.001, variable weight 100–900, from
//     https://fonts.gstatic.com/s/inter/v20/UcC73FwrK3iLTeHuS_nVMrMxCp50SjIa1ZL7.woff2
//   - static/fonts/jetbrains-mono.woff2 is JetBrains Mono 2.211, variable
//     weight 100–800, from
//     https://fonts.gstatic.com/s/jetbrainsmono/v24/tDbV2o-flEEny0FZhsfKu5WU4xD7OwE.woff2
//
// web/tailwind.css names them by hashed file name, so replacing a font means
// updating its url() there too; TestCSSURLsResolve catches a stale one.
//
// The icons in icons.go are from Lucide 1.70.0 (LICENSE-lucide.txt).
package ui
