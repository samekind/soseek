# soseek administration UI

A Chinese React + TypeScript administration UI for the existing same-origin
`/admin/api` endpoints. Vite builds static assets under `/admin/`; navigation uses
URL hashes and needs no client-side route fallback beyond the entry page.

```sh
pnpm install --frozen-lockfile
pnpm test
pnpm build
```

`packageManager` pins pnpm 11.25.0. Production output is `dist/`; the Go packaging
script is maintained outside this directory. The frontend does not embed a
server address, demo data, an administrator token, or upstream API credentials.
A standalone Vite preview can render the login page, but authenticated behavior
requires the same-origin Go admin API. No paid-query playground is included.

## Components and visual system

`src/components/ui/` contains actual **shadcn/ui new-york-v4 registry** source,
using Radix UI primitives. Components were fetched from the official registry at
`https://ui.shadcn.com/r/styles/new-york-v4/COMPONENT.json`. The direct registry
source was used after the CLI request failed with a closed socket. Only registry
module imports were adapted: `cn` resolves to `@/lib/utils`, and component imports
resolve to `@/components/ui`. `.registry/` is the local download cache and is not
required to build.

Registry components: button, card, dialog, alert-dialog, input, label, badge,
table, select, dropdown-menu, sheet, separator, skeleton, alert, checkbox,
tooltip. `components.json` preserves the registry configuration for further
updates. The design uses a white/near-white canvas, neutral borders, Geist with
system Chinese font fallbacks, and blue accents for selection and information.

## Session and safety behavior

- Session authentication uses same-origin cookies; tokens are not stored in
  `localStorage` or `sessionStorage`.
- Every mutation carries `X-Soseek-Admin: 1` and is sent only once. Failure does
  not trigger an automatic mutation retry.
- Newly created or rotated access tokens are held in React memory for their
  one-time display. Closing that dialog requires an acknowledgement that the
  token has been saved. Upstream key values are never fetched for display.
- Ordinary callers cannot promote themselves. The current administrator's
  rotate/revoke actions are disabled to avoid losing administrative access.
- Result URLs become links only for HTTP(S). Answers, snippets, and raw JSON
  render as text, not injected HTML.
- Destructive actions use accessible confirmation dialogs. Forms have labels,
  error states, and busy states; mobile navigation uses a Radix Sheet.

The dashboard distinguishes the UTC day's task count from retained-period
completion/failure totals. Monetary values remain separated into reported,
estimated, and reserved amounts; missing data is displayed as unknown. It does
not describe the reserve as a final bill or channel configuration as a live
health check.

Unit tests cover safe result links, same-origin/CSRF mutation requests, no retry,
and missing-vs-zero monetary display. Full layout, keyboard, login, and mutation
acceptance should use the real local Go fixture or deployed API, not fabricated
production responses.
