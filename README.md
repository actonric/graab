# Graab

A small playground for the CSS-customizable HTML `<select>` element.

Built from the Hacker News thread
[“The `<select>` element can now be customized with CSS”](https://news.ycombinator.com/item?id=43532967),
which discusses the Chrome for Developers article of the same name (Chrome 135, April 2025).

Every dropdown on the page is a real, native `<select>`. There is no JavaScript
widget and no ARIA re-implementation. In browsers that support
`appearance: base-select`, the button, the picker popover, the options and the
checkmark are styled with plain CSS. Everywhere else the browser renders its
built-in control.

## Run it

It is a static site with no build step.

```sh
npm start          # serves ./ on http://localhost:8080 via http-server
```

Or open `index.html` directly in Chrome 135+ / Edge 135+.

## What the demos cover

| # | Demo | Feature shown |
|---|------|---------------|
| 1 | Sort by | Opting in with `appearance: base-select` on the select and on `::picker(select)` |
| 2 | Favourite fruit | Rich HTML inside `<option>`, `<button><selectedcontent>` mirroring the chosen option into the closed control |
| 3 | Assignee | Inline SVG avatars, `<optgroup>`, disabled options, `::checkmark` moved to the end of the row |
| 4 | Theme colour | `:open`, `::picker-icon`, `@starting-style` entry and `allow-discrete` exit transitions on the popover |
| 5 | Customized vs native | Same markup with and without the class, and `@supports (appearance: base-select)` for progressive enhancement |

## The essential CSS

```css
@supports (appearance: base-select) {
  select.graab,
  select.graab::picker(select) {
    appearance: base-select;
  }

  select.graab::picker(select) {
    border: 1px solid #ccc;
    border-radius: 12px;
    box-shadow: 0 10px 30px -10px rgb(0 0 0 / .25);
    transition: opacity .18s, translate .18s,
                display .18s allow-discrete, overlay .18s allow-discrete;
  }
  select.graab:not(:open)::picker(select) { opacity: 0; translate: 0 -.35rem; }
  @starting-style {
    select.graab:open::picker(select) { opacity: 0; translate: 0 -.35rem; }
  }

  select.graab option { display: flex; align-items: center; gap: .6rem; }
  select.graab option::checkmark { order: 1; margin-inline-start: auto; content: "✓"; }
  select.graab:open::picker-icon { rotate: 225deg; }
}
```

See [`styles.css`](styles.css) for the full, commented version.

## Files

- `index.html` – the five demos plus notes
- `styles.css` – page styles and the customizable-select block
- `script.js` – optional: support banner, value readout, live accent colour
- `test/smoke.cjs` – Playwright smoke test (see below)

## Test

The smoke test launches headless Chromium, confirms `base-select` support,
checks that the rich markup survived parsing, opens a picker, chooses an option
with the mouse and with the keyboard, verifies `change` fires, and writes
screenshots to `test/screenshots/`.

```sh
npm install        # pulls playwright; or use a global install:
NODE_PATH=$(npm root -g) npm test
```

Chromium 135 or newer is required. Set `CHROMIUM_PATH` to point at a specific
binary if Playwright's bundled browser is not available.

## Browser support

- Chromium 135+ (Chrome, Edge, Opera, Brave): fully customized.
- Firefox, Safari: native control. The `<button>`, `<selectedcontent>` and inline
  markup inside options are dropped by the older HTML parser, leaving the option
  text, so the fallback is a normal select with the same values.

Feature-detect at runtime with `CSS.supports("appearance", "base-select")`.
