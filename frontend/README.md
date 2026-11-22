# Frontend

React 19 + TypeScript + Vite SPA, built into `mailer/gui/frontend/dist` and embedded in the Go binary by [`mailer/gui`](../mailer/gui/gui.go). [`src/api.ts`](src/api.ts) wraps the generated client; types and helpers in [`src/api/generated/`](src/api/generated/) come from the OpenAPI specs (see [API.md](../docs/API.md#generated-api-artifacts)).

Commands: `npm ci`, `npm run dev` (Vite; no `/api` proxy), `npm run lint` (oxlint), `npm run test` (vitest), `npm run build`. Tooling is invoked through npm scripts, never `npx` ([Development](../docs/DEVELOPMENT.md#no-npx)).

Tests are colocated under [`src/`](src/); native dialogs are forbidden and destructive actions use [`ConfirmDialog`](src/components/ConfirmDialog.tsx) ([Testing](../docs/TESTING.md#protected-invariants), [Design](../docs/DESIGN.md#frontend-ui-conventions)).

Regenerate the API client with `./run.sh generate:api` from the repository root; `./run.sh check:generated` detects stale output ([Development](../docs/DEVELOPMENT.md#api-interface-generation)).
