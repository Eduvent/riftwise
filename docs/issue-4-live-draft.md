# Title: Define automatic live champion-select assistance and acceptance timelines

## Goal

Define champion-select assistance that updates automatically from observed draft state and presents multiple champion recommendations with reasons on a second screen or overlay. The player should not submit a per-match request, role form or candidate list.

**#4 remains contract/fixture product definition before runtime implementation.** Authored artifacts are ready for review, not evidence of human approval, implemented LCU capture, validated ranking or an operational overlay. Keep this issue open while its approval gates are pending.

## Product behavior to review

- Detect the local assigned role when observable. A reported assignment update after an explicit position swap should refresh advice; pick-order/champion swaps alone are not proof of a role change. Informal chat swaps may remain invisible.
- Start from catalog-backed availability, not a user-provided shortlist or arbitrary 1–5 input bound. Optional saved per-role mains prioritize preferences without excluding other eligible champions: Ahri/Vex middle still permits Annie and other non-mains.
- Infer **probable** enemy lanes from observed champions and trusted curated/statistical role-frequency context, preserving flex alternatives and feasible team assignments. Unknown data stays unknown; do not identify hidden players.
- Refresh on picks, bans, observable role changes and relevant context changes. Pause/clear actionable advice on loss, stale, inconsistent or out-of-order state. Recover only with fresh coherent data; clear old advice at draft end/new session.
- Distinguish waiting, collecting, live and unavailable presentation. No “invalid input” blame, invented confidence, performance claims or match-outcome probabilities. Keep source/version/patch internally, not repeated source disclosures on every card.

The product is not limited to alphabetical structural notes. Multiple reasoned recommendations are desired; ranking formula, empirical efficacy and source selection require later approval. Synergy, counters and AD/AP composition remain the long-term analysis roadmap.

## Review artifacts

These branch-qualified links work before merge; they identify the proposal, not a published/approved product:

- [Live contract and reproducible passive inspection](https://github.com/Eduvent/riftwise/blob/docs/4-manual-draft-contract/docs/live-draft-contract.md)
- [Synthetic event timelines with complete expected outputs](https://github.com/Eduvent/riftwise/blob/docs/4-manual-draft-contract/docs/draft-fixtures.json)
- [README implementation boundary](https://github.com/Eduvent/riftwise/blob/docs/4-manual-draft-contract/README.md)
- [Separate live rune enhancement proposal](https://github.com/Eduvent/riftwise/blob/docs/4-manual-draft-contract/docs/rune-suggestions-follow-up.md) — link the exact follow-up issue after publication.

The obsolete manual Draft proposal is now only a superseded pointer. The existing manual **item/recipe planner** and its curated rune copy remain independent and unchanged.

## Acceptance checklist

### Authored artifacts — not human sign-off

- [x] Document the automatic happy path, internal producer/evaluator/consumer responsibilities and user-visible states.
- [x] Separate catalog availability from optional saved mains, retaining a non-main option in the middle example.
- [x] Provide deterministic synthetic timelines covering role detection/swaps/unknowns, probable flex lanes, picks/bans, stale/recovery, contradictory/out-of-order state and session boundaries, with complete stored expected outputs.
- [x] Replace the manual-spec entry point and update the README without changing runtime/API/CLI contracts.
- [x] Draft a separate live rune enhancement rather than silently adding it to #4's implementation scope.

### Pending human decisions and technical proof

- [ ] Review and approve/narrow the product behavior and exact fixture expectations; passive consistency checks do not satisfy this gate.
- [ ] Review ranking/evidence criteria and source-selection requirements before approving a ranking method or efficacy claims.
- [ ] Confirm future implementation scope separately, including second-screen/overlay delivery and safe missing-data behavior.
- [ ] Before LCU integration, verify current Riot policy/endpoint registration requirements and real-client role, explicit-swap, blank-role, availability and freshness behavior.
- [ ] Replace the rune proposal link with the exact published enhancement issue number after publication.

## Feasibility and boundaries

Community LCU examples expose `localPlayerCellId`, `myTeam[].cellId` and `assignedPosition` in `GET /lol-champ-select/v1/session`, including blank values; community documentation lists position-swaps and pick-order-swaps separately. This supports a detection hypothesis, not proof that today's client updates assignment after a swap. No real-client session was captured for these fixtures.

The [official League Client API documentation](https://developer.riotgames.com/docs/lol#league-client-api) describes the LCU API as unsupported and asks developers to register endpoint usage. Policy compliance is a future integration prerequisite, neither blanket permission nor an invented blanket ban on recommendations.

Out of scope: runtime observer/evaluator, HTTP endpoint, ranking algorithm, overlay host/UI, data-source integration, persistent player-data changes and automatic game actions. Preserve canonical catalog resolution; item scores/profile confidence are not champion-selection evidence. The JSON is hypothetical acceptance data, not engine execution or measured gameplay results.
