# Issue #3 — Real Live Client validation
Branch: `fix/3-live-practice-validation`; worktree: `.atl/worktrees/issue-3`; delivery: `ask-on-risk`, `chain_strategy=feature-branch-chain` (no publishing authorized).
Checks: `go test ./...`, focused Go tests, Node dashboard tests, and sanitized real Practice Tool/browser observations. Actual forecast: at least 663 authored diff lines (T1=125, T2=538), excluding generated 574-line catalog fixture and remaining live evidence. Slices: mode e49cdd3; coherent profile+tests/data next; live evidence last.

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
T1 | S1-S2,S9 | inline integration of previously tested worker fix | done | Isolated and transferred only mode fix/tests/README; root untouched. RED exit1; GREEN exit0; full Go suite and 33 frontend tests exit0. Commit: e49cdd3 (125 authored diff lines).
T2 | S4,S7,S10 | delegated writer (context backstop) | in_progress | Implementation, RED/GREEN, Go suite/vet/build and real ready state verified; exact16.22.1 Ahri default, unknown role preserved. Delivery shape selected; preparing coherent profile commit.
T3 | S3-S7,S9 | inline operator-assisted validation | blocked | Isolated backend job-7 connected with real ready recommendation. Awaits coordinated human purchases, browser observations, outage/recovery, terminal/waiting and installed-patch evidence; no such cases marked passed. Commit: pending evidence.
T4 | S8-S10 | inline evidence/documentation | pending | Record per-criterion pass/fail/untested, defects and next steps; finish applicable checks and reviewable local commits without push/merge/closure. Commit: pending.

## Log
L1. Original user request: "Ok empecemos la issue #3 dime que se necesita. Si es que necesitas que corra una partida de practica etc."
L2. User: "Listo lol esta corriendo con la partida de practica iniciada". Real Live Client attached to Ahri, position NONE, PRACTICETOOL, map11. Initial waiting state was not observed.
L3. User: "Ok autorizo el ajuste". Mode-only fix completed in shared root. Definitive RED exit1, GREEN 21 cases exit0, `go test ./...` exit0. Real backend then passed mode validation but failed profile availability for catalog16.22.1. No source-game version returned; catalog patch is not evidence of installed patch.
L4. User: "Entonces resolvamos esta issue en una nueva rama para no pisar a issue 1 y 2 que pueden aun no terminar." Created isolated worktree and branch, fast-forwarded from stale local main984836a to recorded origin/main17f30d6 containing #1/#2. Shared root HEAD/index/dirty files untouched. No push, merge, PR or issue closure authorized.
L5. Prior native assessment was unavailable because untracked scope was undeclared; no native approval claimed. Existing backend job-3 runs from root with dedicated data/issue-3-validation.db and Riot disabled; replace only this backend when isolated candidate is ready. Human actions and browser observation are required for live completion; never substitute fixture success for that proof.
L6. T1 committed as e49cdd3 from isolated worktree after observed RED/GREEN, full Go suite and 33 Node tests (job-4 exit0). Native committed-range assessment: medium, reviewDue=false/under_budget, no independent verifier; no review approval claimed. Mirror saved as Engram5826; full-observation readback unavailable because tool argument validation disagrees with advertised schema; local file readback succeeded.
L7. Parent downloaded official16.22.1 es_MX/item.json and en_US/championFull.json (job-5 exit0); exact URLs, SHA256, normalization/pruning and scope recorded in fixture provenance. Worker had no background tool, so parent owned downloads/full checks; worker performed local RED/GREEN only. T2 adds Ahri default + schema-required reactive neutral only; previous16.19 behavior and strict cross-minor rejection preserved. Parent Go suite/vet/build/diff check job-6 exit0. Native assessment medium/reviewDue=false; no review approval claimed.
L8. Real isolated backend job-7: Ahri/NONE, PRACTICETOOL map11, clock2357.2s, gold5693.79, trinket3340 only. Recommendation ready; rules generic_external_exact16.22.1, profile ahri_default/champion_default, role_unavailable and champion-default advisories, missing enemy traits caveat; target Liandry6653 (3000), component RubyCrystal1028 (400), gold_needed0. Installed version still absent/unknown. No purchases/recovery/end/browser pass claimed.
L9. Honest split: mode unit125 authored lines committed; smallest coherent profile unit538 authored lines (registry+tests+provenance/docs) pending commit. Generated catalog574lines/18865bytes excluded. Delivery shape choice required before next commit, with no publishing authority implied. Shared root dirty files unchanged and no cleanup performed.
L10. User selected "Cadena sobre rama de la #3 — recomendada" (`chain_strategy=feature-branch-chain`). Cache this shape for future delivery; no push/PR/merge authorized. Profile slice retains data+tests together despite advisory400-line overage; no cosmetic shrinking or artificial separation.
