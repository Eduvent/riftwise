# Issue #3 — Real Live Client validation
Branch: `fix/3-live-practice-validation`; worktree: `.atl/worktrees/issue-3`; delivery: `ask-on-risk` (no publishing authorized).
Checks: `go test ./...`, focused Go tests, Node dashboard tests, and sanitized real Practice Tool/browser observations. Forecast: approximately 400 authored diff lines, excluding generated catalog fixtures; revise from actual work-unit evidence before delivery.

## Specs
S1. User: "Entonces resolvamos esta issue en una nueva rama para no pisar a issue 1 y 2 que pueden aun no terminar."
S2. User: "Ok autorizo el ajuste" — authorized adjustment from the preceding exchange: "Si autorizás el ajuste para admitir práctica en el mapa 11, seguimos ahí."
S3. Issue #3: "Verify waiting state, attachment to a match and live telemetry updates."
S4. Issue #3: "Verify gold, inventory changes and recommendation affordability updates against observed game state."
S5. Issue #3: "Verify disconnect/stale presentation and same-match recovery without duplicate semantic events."
S6. Issue #3: "Verify match end and transition back to waiting or the next match."
S7. Issue #3: "Record installed game patch, selected catalog patch, active rule/profile provenance and any fallback warnings."
S8. Issue #3: "Record pass/fail evidence and create narrowly scoped follow-ups for confirmed defects; do not label untested cases as passed."
S9. Issue #3 scope: "Use the normal live dashboard with League of Legends Practice Tool. Keep optional account enrichment disabled unless separately consented. Record sanitized results without raw player identifiers or credentials."
S10. Issue #3 out of scope: "Claiming strategic efficacy, expanding all champion profiles, benchmarking a Draft model or proving overlay fullscreen support."

## Tasks
T1 | S1-S2,S9 | inline integration of previously tested worker fix | in_progress | Isolate branch/worktree; transfer only mode guards, regression tests and README bullet. Preserve root checkout. Commit: pending checks.
T2 | S4,S7,S10 | delegated writer (context backstop) | pending | Resolve observed Ahri/default-profile compatibility with catalog 16.22.1 using official catalog evidence and deterministic tests; retain strict patch boundaries and unknown role/version. No all-champion expansion. Commit: pending.
T3 | S3-S7,S9 | inline operator-assisted validation | pending | Run sanitized live checks on isolated backend, then coordinate gold/purchases, browser presentation, controlled outage/recovery, terminal/waiting and installed-patch evidence. Commit: pending evidence.
T4 | S8-S10 | inline evidence/documentation | pending | Record per-criterion pass/fail/untested, defects and next steps; finish applicable checks and reviewable local commits without push/merge/closure. Commit: pending.

## Log
L1. Original user request: "Ok empecemos la issue #3 dime que se necesita. Si es que necesitas que corra una partida de practica etc."
L2. User: "Listo lol esta corriendo con la partida de practica iniciada". Real Live Client attached to Ahri, position NONE, PRACTICETOOL, map11. Initial waiting state was not observed.
L3. User: "Ok autorizo el ajuste". Mode-only fix completed in shared root. Definitive RED exit1, GREEN 21 cases exit0, `go test ./...` exit0. Real backend then passed mode validation but failed profile availability for catalog16.22.1. No source-game version returned; catalog patch is not evidence of installed patch.
L4. User: "Entonces resolvamos esta issue en una nueva rama para no pisar a issue 1 y 2 que pueden aun no terminar." Created isolated worktree and branch, fast-forwarded from stale local main984836a to recorded origin/main17f30d6 containing #1/#2. Shared root HEAD/index/dirty files untouched. No push, merge, PR or issue closure authorized.
L5. Prior native assessment was unavailable because untracked scope was undeclared; no native approval claimed. Existing backend job-3 runs from root with dedicated data/issue-3-validation.db and Riot disabled; replace only this backend when isolated candidate is ready. Human actions and browser observation are required for live completion; never substitute fixture success for that proof.
