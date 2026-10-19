# Frontend design contract (shadcn/ui)

Every screen in `frontend/src` is being migrated from hand-rolled Tailwind markup
to shadcn/ui primitives. This file is the spec workers follow. Read it fully
before editing a page.

## The job

Rewrite the **presentation** of the page assigned to you. Keep behaviour exactly:
every `fetch`, store call, handler, effect, prop and piece of derived data stays.
You are replacing markup and classes, not rewriting the feature.

## Foundation already in place

| what | where |
| --- | --- |
| theme tokens + `.page`, `.eyebrow`, `.font-display` | `src/index.css` |
| page furniture: `PageHeader`, `SectionTitle`, `EmptyState`, `Field`, `Stat` | `src/components/page-shell.jsx` |
| primitives | `src/components/ui/*.jsx` (button, card, badge, input, label, textarea, select, switch, checkbox, tabs, dialog, alert-dialog, dropdown-menu, table, separator, skeleton, tooltip, scroll-area, sheet, progress, avatar, collapsible, accordion, radio-group, toggle, toggle-group, sonner) |
| reference implementation of the target style | `src/pages/Login.jsx` |
| shell (rail + header + tab bar) | `src/App.jsx` |

Read `Login.jsx` before you start: it shows the header rhythm, field style,
card style and button usage the other pages must match.

## Design language (strict, quiet, technical)

- **Surfaces**: `bg-background` page, `bg-card` for panels, `bg-muted` for
  inset rows. Borders are `border-border` hairlines. No gradients, no glow, no
  drop shadows on cards (`shadow-none`), no rounded-2xl/3xl blobs — `rounded-lg`.
- **Type**: `font-display` for headings, default sans for body, `font-mono
  tabular-nums` for numbers, identifiers, ports, slugs and code. Micro labels
  are `text-[10px] uppercase tracking-[0.12em] text-muted-foreground`.
- **Density**: page padding comes from the `.page` class. Section gap `gap-6`,
  card padding `p-5`, toolbar row height `h-9`.
- **One green thing per view**: primary button, active state, healthy/running
  dot, key metric. Everything else neutral: `text-muted-foreground` for
  secondary copy, `text-foreground` for facts.
- **Status colours** only as tiny dots/badges: `--success`, `--warning`,
  `--error` (Tailwind: `text-success`, `bg-warning`, `border-error`…). Never
  paint whole panels red/green.
- **Motion**: at most `transition-colors`. No entry animations on migrated
  pages (do not add `animate-fadeIn`).
- Keep all user-visible copy — strings, labels, tooltips, empty-state text.
  Translate nothing, rename nothing.

## Page anatomy

```jsx
<div className="page">
  <PageHeader eyebrow="…" title="…" description="…" actions={<Button …/>} />
  <SectionTitle hint="…" right={…}>Section</SectionTitle>
  …content…
</div>
```

- A list of entities → `Card` grid (`grid gap-3 md:grid-cols-2 xl:grid-cols-3`).
- Tabular data (users, files, models with columns) → shadcn `Table` with
  `TableHeader` in micro-uppercase style.
- Destructive actions → `AlertDialog`; forms and detail forms → `Dialog` or
  inline `Card`.
- Toggles → `Switch`; exclusive choices → `Tabs` or `ToggleGroup`; free choice
  from many → `Select`.
- Long-running/missing data → `Skeleton` rows, not a spinner-only screen.
- Empty state → `EmptyState`.
- Copy-a-value rows → shadcn `Button variant="outline" size="icon"` with a lucide
  icon; **reuse `components/CopyField.jsx`** where the page already uses it.

## Forbidden

Do **not** edit: `src/store.js`, `src/auth.js`, `src/lib/**`, `src/hooks/**`,
`src/index.css`, `src/App.jsx`, `src/components/page-shell.jsx`,
`src/components/ui/**`, other pages, `package.json`, `vite.config.js`.
If a primitive is missing, compose what exists — do not run the shadcn CLI
without being told to.

Do not change API endpoints, request bodies, or query keys. Do not rename
components or their exports: other files import them.

## Acceptance (run it yourself, paste the output in your final message)

```bash
cd frontend
env -u NODE_ENV npm run build          # must succeed
npx eslint <the files you touched>     # no NEW errors (react-refresh
                                       # only-export-components errors are
                                       # pre-existing in RecipeCard/Agents)
grep -c 'fetch\|useStore\|onClick' <page file>   # logic still present
```

Then confirm the page still renders the same facts: list the main headings and
the data-driven elements you kept.
