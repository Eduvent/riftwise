# Title: [P1] Define automatic live champion-select assistance and scored acceptance timelines

## Outcome

Define champion-select assistance that updates automatically from observed draft state and presents multiple champion recommendations with numeric scores and reasons on a second screen or overlay. The player does not submit a per-match request, role form or candidate list.

**Product behavior and synthetic fixtures are approved with the 5.0-to-0.0 scoring correction.** #4 is a product-definition issue, not a runtime delivery issue. Approval does not validate a formula, data sources, gameplay efficacy, LCU endpoint behavior, policy compliance or an operational overlay. Research and implementation acceptance are separate from completion of this definition.

## Approved product behavior

- Give every displayed suggested pick a finite numeric score within **0.0..5.0 inclusive**. Higher is a stronger internal evaluation: 5.0 highest, 0.0 lowest. Display cards in descending (non-increasing) score order. Scores are internal valuations, not probabilities or calibrated confidence; no formula, factors/weights, thresholds or tie policy is selected here.
- Detect the local assigned role when observable. A reported assignment update after an explicit position swap should refresh advice; pick-order/champion swaps alone are not proof of a role change. Informal chat swaps may remain invisible.
- Start from catalog-backed availability, not a user-provided shortlist or arbitrary 1–5 input bound. Optional saved per-role mains prioritize preferences without excluding other eligible champions: Ahri/Vex middle still permits Annie and other non-mains.
- Infer **probable** enemy lanes from observed champions and trusted curated/statistical role-frequency context, preserving flex alternatives and feasible team assignments. Unknown data stays unknown; do not identify hidden players.
- Refresh on picks, bans, observable role changes and relevant context changes. Pause/clear actionable advice on loss, stale, inconsistent or out-of-order state. Recover only with fresh coherent data; clear old advice at draft end/new session.
- Distinguish waiting, collecting, live and unavailable presentation. Missing context/evidence is not score 0.0: keep actionable cards empty while collecting required context or unavailable. No “invalid input” blame, invented confidence, performance claims or match-outcome probabilities. Keep source/version/patch internally, not repeated source disclosures on every card.

The product is not limited to alphabetical structural notes. Multiple reasoned recommendations are desired; ranking formula, empirical efficacy and source selection require later evidence and approval. Synergy, counters and AD/AP composition remain the long-term analysis roadmap.

## Definition artifacts

These links target the intended **main** integration destination. They are not a claim that this correction has already merged or that this issue has closed; publication/integration belongs to the parent workflow.

- [Live contract and reproducible passive inspection](https://github.com/Eduvent/riftwise/blob/main/docs/live-draft-contract.md)
- [Synthetic event timelines with complete expected outputs](https://github.com/Eduvent/riftwise/blob/main/docs/draft-fixtures.json)
- [README implementation boundary](https://github.com/Eduvent/riftwise/blob/main/README.md)
- [First Draft engine investigation scope](https://github.com/Eduvent/riftwise/blob/main/docs/draft-engine-investigation.md). **Research issue number pending publication:** this is a scope-document placeholder, not a published issue link; replace it with the actual issue link when the parent publishes the follow-up. The [local scope document](draft-engine-investigation.md) is the authored source.
- [Separate live rune enhancement #7](https://github.com/Eduvent/riftwise/issues/7), with its [scope document](https://github.com/Eduvent/riftwise/blob/main/docs/rune-suggestions-follow-up.md).

The obsolete manual Draft proposal is now only a superseded pointer. The existing manual **item/recipe planner** and its curated rune copy remain independent and unchanged. Overlay delivery stays within [#5](https://github.com/Eduvent/riftwise/issues/5), not this definition's runtime scope.

## Definition acceptance — supported by approval and artifacts

- [x] Approve the automatic product behavior and synthetic fixture expectations with the numeric-scale correction, without approving an algorithm or claiming gameplay proof.
- [x] Specify a numeric score for every displayed pick, inclusive 0.0..5.0 bounds, higher-is-stronger semantics and descending display order, with no score for missing evidence.
- [x] Document the automatic happy path, internal producer/evaluator/consumer responsibilities and user-visible states.
- [x] Separate catalog availability from optional saved mains, retaining a non-main option in the middle example.
- [x] Provide four synthetic timelines and 21 complete stored outputs covering role detection/swaps/unknowns, probable flex lanes, picks/bans, stale/recovery, contradictory/out-of-order state and session boundaries. Scores are authored contract examples, not actual champion evaluations; existing examples include both 0.0 and 5.0.
- [x] Provide passive inspection of score shape, finite numeric values (not booleans), bounds, descending order, both boundaries, preserved card identities/reasons and exact agreement with the inline example. This checks artifacts, not an engine.
- [x] Replace the manual-spec entry point and update the README without changing runtime/API/CLI contracts.
- [x] Keep live rune enhancement [#7](https://github.com/Eduvent/riftwise/issues/7) separate from #4, and author one first feasibility/scoring investigation scope rather than speculative runtime implementation.

Integration, follow-up issue publication and #4 closure are parent-owned delivery steps. This authored body does not assert those operations have happened.

## Separate future research and implementation acceptance

The [investigation scope](draft-engine-investigation.md) defines the first follow-up. Its publication is not research execution; no game session or data collection is required to complete #4. These unchecked items belong to that future work, **not remaining #4 definition gates**:

- [ ] Produce an evidence-backed go/no-go/conditional feasibility report covering real-client roles/swaps, availability, ordering/freshness and current Riot policy/endpoint registration requirements.
- [ ] Assess trustworthy role/recommendation/rune/catalog sources, patch/queue/rank coverage and access/update/licensing/cost/privacy constraints.
- [ ] Propose an explainable 5.0..0.0 score design and mains weighting for later decision, without treating this approval as formula or empirical-efficacy validation.
- [ ] Derive bounded implementation issues and automated plus real-client validation plans from findings; confirm implementation scope separately, coordinating runes with #7 and overlay with #5.

## Feasibility and boundaries

Community LCU examples expose `localPlayerCellId`, `myTeam[].cellId` and `assignedPosition` in `GET /lol-champ-select/v1/session`, including blank values; community documentation lists position-swaps and pick-order-swaps separately. This supports a detection hypothesis, not proof that today's client updates assignment after a swap. No real-client session was captured for these fixtures.

The [official League Client API documentation](https://developer.riotgames.com/docs/lol#league-client-api) describes the LCU API as unsupported and asks developers to register endpoint usage. Policy compliance is a future integration prerequisite, neither blanket permission nor an invented blanket ban on recommendations.

Out of scope: runtime observer/evaluator, HTTP endpoint, ranking algorithm, overlay host/UI, data-source integration, persistent player-data changes and automatic game actions. Preserve canonical catalog resolution; item scores/profile confidence are not champion-selection evidence. The JSON is hypothetical acceptance data, not engine execution or measured gameplay results.
