# Draft follow-up and validation gates

The [conditional feasibility report](draft-feasibility-report.md) closes exploration, not implementation. These are **proposed bounded work packages**, not newly created issues or permission to implement/collect. Preserve the [approved contract](live-draft-contract.md) and its [synthetic acceptance timelines](draft-fixtures.json).

## Recommended sequence and dependencies

Research report → evidence/access decision → supported observer and evaluator → **simple human visual validation** → separately scoped internal randomized laboratory. Source/schema work and observer work may be planned separately, but advice must not become actionable until both supply supported context. None of these packages selects a formula or silently narrows the product to curation-only.

| Package / priority | Dependencies and scope | Acceptance criteria | Explicit non-goals |
| --- | --- | --- | --- |
| **F1: free-source applicability audit** | Use the report's Data Dragon/ChainCC/GPTilt/Oracle leads; choose a bounded, separately permitted sample only after rights review. SoloQ aggregation is a distinct design decision. | Record provenance/license scope, schema/patch coverage, queue/rank/region/season/window, match denominators, duplicates, missing values, update policy, access/rates and privacy. Distinguish professional, SoloQ and personal populations. Unsupported role/rune evidence stays unknown. | No mass scraping, subscriptions, account creation, inference of efficacy, silent license acceptance or reuse of fixture ratings. |
| **F2: macOS observer and lifecycle adapter** | Confirm product/endpoint access boundaries and separately authorize client trials. Reuse catalog identity/snapshots; permit only justified observations. | Record actual client version/queue; map local role/unknown, team picks/bans and supported eligibility without hidden identities. Demonstrate waiting/collecting/live/unavailable, coherent local ordering, safe invalidation/recovery and retired-session isolation. | No gameplay/rune writes, chat parsing, hidden-player discovery, automatic informal-swap detection or exact pick-order gate. |
| **F3: explainable evaluator** | F1 compatible source/context and F2 normalized binding; separately approve scoring approach and mains policy. | Pure reproducible evaluation binds session/observation/catalog/context/preferences; finite inclusive 0.0..5.0, descending cards/reasons, supported probable flex alternatives, eligible non-mains retained. Missing required evidence suppresses advice. | No unapproved weights, probability/efficacy claims, fabricated alternative cards or use of item scores as champion evidence. |
| **F4: simple visual validation surface** | F2/F3 with fresh supported context; second-screen delivery may precede overlay. | User can corroborate local role, allied/enemy picks/bans, suggestions, scores and reasons; automatic updates and visible clearing on loss/end; probable/unknown labels and no per-match form. Internal diagnostics retain source/patch/binding without cluttering cards. | No laboratory prerequisite, new polished UI design in #8 or replacement of overlay #5. |
| **F5: internal Draft laboratory** | After F4 human visual validation; separately approve scope and independent reference cases. | Seeded position-aware random scenarios, replayable seeds and fixed contract cases; preserve flex/unknown context, feasible assignments and legitimate observed information. Separate invariant failures from quality disagreement. | No hidden-role leakage from generator truth; random validity or agreement with historical pro picks is not strategic correctness. |
| **F6: exact pick-order feasibility, low priority** | Deferred user priority; revisit only for a named product need with a better measurement baseline and fresh consent. | Verify UI before/after against unambiguous observed mapping; retain unknown when ambiguous and distinguish order swaps, champion trades and role updates. | No claim it is solved, universally reconstructable, irrelevant to every future recommendation or required before F2–F4. |

Reuse [#7](https://github.com/Eduvent/riftwise/issues/7) for live rune alternatives and [#5](https://github.com/Eduvent/riftwise/issues/5) for macOS overlay feasibility/delivery. Rune acceptance needs valid champion/role/patch configurations, alternatives, conditions/tradeoffs and an applicable source; static rune definitions and existing manual pregame copy are not live recommendation evidence. Overlay acceptance needs macOS behavior and display/privacy checks; it is not implied by browser rendering. Do not create duplicates or implement these packages merely because #8 closes.

## Automated validation proposal

These checks run **when the relevant implementation exists**, using deterministic isolated state and the public interface of that component. Existing fixtures are authored acceptance oracles, not captured gameplay or calculated strength. No Draft engine executed them in this research.

| Rule / cases | Required observable assertions |
| --- | --- |
| **Score and explanation** | Reject nonfinite/out-of-range supported evaluations; both 0.0 and 5.0 are valid boundaries. Display non-increasing scores, stable documented ties once chosen, correct champion identities and readable evidence-backed reasons. Missing required context yields no actionable cards, not 0.0. |
| **Mains and candidate pool** | Ahri/Vex middle preferences do not exclude eligible Annie; absent preferences do not remove the pool. Removed/unresolved mains never create ownership or queue eligibility. Preference changes invalidate old bindings; do not invent a bonus amount. |
| **Picks/bans and eligibility** | Replay accumulated picks/bans from the approved timelines: picked Vex and banned Ahri disappear, while eligible Annie remains. Validate catalog resolution/availability provenance, new/unresolved IDs, zero/one remaining options and unknown ownership; do not pad alternatives or silently discard ambiguous state. |
| **Roles and flex** | Blank, missing and unsupported local assignments remain unknown/collecting, never filled from a main. Observable assignment updates recompute; order swaps/champion trades alone do not change role. Hidden enemy lanes stay probable with feasible alternatives, not forced duplicate lanes. |
| **Loss, stale and ordering** | Disconnect, expired freshness, contradictory state and same-session older work remove advice; reconnect alone is not fresh recovery. Delayed evaluation for an older observation/catalog/context/preference cannot publish. Exact duplicates need not trigger new advice; accepted order never moves backward. |
| **Session boundary** | End clears cards and pending work. A new session may restart its local revision; late retired-session observations/results cannot replace it. Validate both observation and asynchronous result races. |
| **Source compatibility** | Wrong patch/population, sparse or missing required evidence, source failure and catalog/context version changes are visible and conservative. Assert source metadata and excluded stale results without exposing personal identifiers. |
| **Display integration** | Through UI/public response, verify automatic updates, detected/probable/unknown labels, descending cards/reasons and visible clearing across all four states. Browser mocks do not establish macOS overlay behavior. |

Use the approved four timelines as fixed cases; extend only for requirements an implementation actually touches. Once commands/endpoints exist, assert exit/status, exact response/error behavior and unchanged persisted state/counters for rejected input as applicable. Tests must not depend on production accounts, stored raw player data or repository history. Strategic-quality assessment remains a separate independently reviewed study.

## Separately authorized real-client plan

**Not executed or authorized by this document.** Obtain fresh user direction for the exact trial/access and any new endpoint, lockfile location, data collection, recording or disruption. Prior successful reads and report publication are not standing permission for another match.

Before observing, record UTC time, installed client version, actual queue/mode identifier and test conditions; do not substitute catalog patch or user-described mode for measured client metadata. Initial follow-up should declare one supported macOS client/queue pair, not promise every queue. Where identifying it would require unapproved access, record it as unknown and do not claim coverage.

| Trial | Measurements and pass/fail evidence |
| --- | --- |
| **Preflight and timing** | Exact authorized credential source; secrets in memory; authenticated loopback, validated CA/hostname, no proxies/redirects/TLS bypass. State allowlist, cadence, deadlines, response/output caps and stop budget before launch. Outside Draft is a skipped baseline, not successful selection evidence. |
| **Role and context** | Compare human-selected/visible role with local observed assignment; separately test blank/absent/unsupported roles and explicit updates in a safe supported setup. Retain unknown; no request to disrupt a competitive match. Informal agreements remain an untestable detection guarantee. |
| **Picks/bans and eligibility** | Human-correlate visible teams/completed picks/bans and supported available/owned/queue-eligible champions; verify removal of taken/banned candidates. Capture opening/ban phases only when explicitly planned. Fail unsupported eligibility to collecting rather than treating every catalog champion as selectable. |
| **Recovery and races** | Use a safe harness/proxy simulation unless an exact real-client disruption is authorized. Validate timeout/stale invalidation, coherent re-read before advice resumes, late results, end/new selection and retired-session rejection. Do not call non-atomic GETs a native revision guarantee. |
| **Visual trial before laboratory** | With F4, user confirms identities, local role, detected versus probable labels, scores/reasons and prompt clearing/replacement while picks change and at exit. Log pass/fail per criterion and source/patch applicability; agreement with advice is not proof of its efficacy. Reuse #5 for separate overlay-specific checks. |
| **Swaps and order, if later scoped** | Separate explicit role updates, champion trades and low-priority order changes. Establish an unambiguous observable baseline and human before/after evidence before asking for a trial; absent mapping is unknown, not success. |

Privacy/output: publish only allowlisted summaries and necessary completed champion data; no credentials, account/player/cell/session identifiers, chat, raw responses or private filesystem paths. Retain/expire local diagnostics under the approved trial plan. Do not download population datasets or contact additional endpoints as an incidental preflight step.

Stop on authentication/TLS failure, first request error, unexpected/sensitive payload, incoherent observation, output/time budget exhaustion, user stop or selection exit. Remove actionable advice on invalidity; do not auto-restart, bypass controls or ask for another game until measurement limitations are resolved. Missing/failed/untestable checks are recorded explicitly, not marked passed. Worktrees isolate files/staging, not client sessions, ports, SQLite or processes; coordinate real-client use and separate `LOL_ENGINE_LISTEN_ADDR` / `LOL_ENGINE_DB_PATH` for future running applications.

## Research acceptance map

| Issue 8 criterion | Delivered evidence / remaining condition |
| --- | --- |
| **Conditional conclusion and evidence classification** | Report decision, dated E1–E9 and method/limitations; observed scope is deliberately narrow. |
| **Sources, access, policy and privacy** | Report comparison and dated R1–R13, explicit failed extractions, rate/rights/coverage unknowns and policy/registration boundary. |
| **Scoring and mains tradeoffs** | Report's three approaches and mains alternatives; no selected formula or efficacy claim. |
| **Bounded follow-ups with dependencies/non-goals** | F1–F6 above, with #5/#7 reuse; proposals only, not speculative issue creation. |
| **Automated plan** | Rule table above and approved fixtures; future functional evidence, not an implemented engine. |
| **Real-client plan** | Fresh authorization, version/queue coverage, method, privacy, stop/pass/fail rules and untestable cases above. |

Closure means the investigation report and plans are delivered. All implementation/source approvals and unproven behavior remain follow-up gates; none becomes true because a checkbox or issue changes state.
