# Ludus plugin UI style

Adapted from the Ludus GUI style guide for resource plugins. Start with
[`source/ui/index.html`](source/ui/index.html) and extend only what your plugin needs.

## Scope and theme

- The host provides navigation, the plugin title, and a default range selector.
  Build only the content area; do not repeat the plugin name in a banner or add
  another Resources / Plugins breadcrumb.
- If range selection belongs with target/action controls, use the optional host
  `selectRange` bridge and `context.ranges`, with a labeled picker marked
  `data-ludus-range-picker`. The current host then hides its outer selector.
  Feature-detect this support and retain a usable fallback on older hosts.
  Never provide both a working embedded picker and a second custom range toolbar.
- Keep all CSS, JavaScript, and assets inside `ui/index.html`. External styles,
  scripts, and fonts are blocked. See [PLUGIN_API.md](docs/PLUGIN_API.md) for bridge details.
- Read `theme` from `await window.ludus.context()` and apply it to the document.
  Handle `ludus:context` events so theme changes take effect without a reload.
- Support both dark and light themes. Use dark until the host context arrives.

## Visual identity

Use a compact technical interface: monospace text, square corners, thin borders,
quiet surfaces, and muted gold for actions, selection, and keyboard focus.
Keep data and controls dominant. Optional corner brackets can accent a panel;
large gradients, neon effects, rounded cards, and decorative animation do not fit.

Use the example's semantic CSS variables rather than hardcoded component colors:

| Variable | Dark | Light | Purpose |
| --- | --- | --- | --- |
| `--background` | `#090a0c` | `#f7f6f2` | Canvas |
| `--card` | `#101114` | `#fbfaf8` | Panels |
| `--foreground` | `#f4f4f1` | `#2a2622` | Main text |
| `--muted` | `#a1a4aa` | `#7c736a` | Secondary text |
| `--border` | `#2e3138` | `#d7d2cc` | Dividers and outlines |
| `--primary` | `#c6a153` | `#bf9740` | Actions and focus |
| `--danger` | `#dc2828` | `#c73333` | Errors and destructive actions |

Gold buttons use dark text as in the example. Check text and control contrast in
both themes, including muted and error text; adjust a specific use if needed.
Reserve green, red, yellow, and blue for status, always accompanied by a text label.

## Typography and layout

- Use `"JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, Consolas, monospace`
  everywhere, including form controls. Let the local font fallback work; do not add font assets.
- Body text: 14px with 1.5 line height. Titles: 18px. Metadata: 11–12px.
  Use sentence case for labels and actions; uppercase only for short section or status labels.
- Use a 4px spacing grid: 8–12px between controls, 16–24px between sections,
  12–20px panel padding, and about 24px outer padding (less on narrow screens).
- Use square corners and 1px borders. Separate surfaces with color and spacing;
  avoid deep shadows and nested padded panels.
- Let controls wrap at narrow widths. Wrap long identifiers or give tables and
  code their own horizontal scroll area. Avoid horizontal scrolling of the whole page.

## Controls and accessibility

- Keep controls compact: 32px minimum hit area, typically 36–40px high,
  with 8–12px horizontal padding. Use one clear primary action per group.
- Use native buttons, inputs, labels, and table headers. Give icon-only buttons
  accessible names; show essential actions without requiring hover.
- Provide a visible 2px gold `:focus-visible` outline with an offset.
  Preserve keyboard navigation and readable disabled, hover, and selected states.
- Place field labels above inputs and concise help or validation text below them.
  Use text alongside status color so meaning survives without color perception.
- Prefer `window.ludus.confirm()` for confirmation and `window.ludus.toast()` for
  brief feedback. Custom dialogs must manage focus, Escape, and focus restoration.
- Keep any motion subtle and respect `prefers-reduced-motion`.

## Content and request states

- Write direct labels such as `Refresh status` or `Download report`. Explain only
  what the user needs to act; keep implementation details out of the interface.
- Loading: show a short progress message, preserve the layout, and prevent duplicate
  submissions. Announce progress through an appropriate live region or busy state.
- Empty: state what is missing and provide the next useful action, such as selecting
  a range or creating the first item. An empty result is not an error.
- Error: describe the failed action, preserve useful input, and offer retry when
  possible. Use an alert for actionable errors; never display credentials or tokens.
- Success: show the updated result or a brief confirmation and restore enabled controls.

Before shipping, check the UI in both themes, at narrow widths, with a keyboard,
and with loading, empty, error, and success results. The example is the starting
point for styling and bridge wiring, not a complete UI for every plugin.

On range changes, immediately clear stale range data and ignore responses from
previous requests. A theme-only context event must not reset the selected range
or repeat a data request. Keep field labels above controls and let toolbar items
wrap on narrow screens.
