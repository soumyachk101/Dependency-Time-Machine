# Typography

Space Grotesk (display + UI) and Geist Mono (identifiers, versions,
timings, counts) are the two families for this product, per the design
brief's recommended set for Dependency Time Machine.

## Self-hosting

Both families are OFL (Space Grotesk) and a permissive custom licence
(Geist, by Vercel — check the current terms before shipping). Drop the
licensed `.woff2` files into `frontend/src/styles/fonts/` as:

- `SpaceGrotesk-Regular.woff2` (400)
- `SpaceGrotesk-Medium.woff2` (500)
- `GeistMono-Regular.woff2` (400)

then register them in `tokens.css`:

```css
@font-face {
  font-family: "Space Grotesk";
  src: url("./fonts/SpaceGrotesk-Regular.woff2") format("woff2");
  font-weight: 400;
  font-display: swap;
}
```

Until the licensed files are vendored, the CSS fallback stacks in
`tokens.css` apply (Helvetica Neue / system sans for display+body,
SFMono/ui-monospace for code). Layout is sized in `rem` so nothing breaks
if the custom faces load later.

## Roles

- **Display / headings** — Space Grotesk Medium, tight leading (1.08).
- **UI / body** — Space Grotesk Regular, comfortable leading (1.5),
  14–16px.
- **Code / identifiers / metrics** — Geist Mono, tabular numerals
  (`font-variant-numeric: tabular-nums` set globally). Use for versions,
  durations, counts and any value that lines up in a column.
