# Title: [P1] Investigate live Draft feasibility, evidence sources and explainable scoring

## Decision to enable

Produce one evidence-backed **go / no-go / conditional** feasibility report before proposing a live Draft engine implementation. [#4](https://github.com/Eduvent/riftwise/issues/4) approves product behavior and synthetic fixtures, including ratings from 5.0 (highest) to 0.0 (lowest), not a formula, trustworthy sources, gameplay efficacy, client feasibility or policy compliance.

This document defines one first investigative follow-up. Publishing the issue is **not executing the research**. No game session, client capture, data collection or runtime implementation occurs as part of this documentation task. Do not create speculative implementation issues before the report identifies bounded, supported work.

## Investigate

| Area | Questions and evidence required |
| --- | --- |
| Real-client feasibility | Verify available observations of session/local identity, assigned roles (including blank/absent values), explicit position updates, pick-order swaps, champion trades and unobservable informal swaps. Record client version, queue, conditions and limits; do not promote community examples to current-client proof. |
| Availability and lifecycle | Establish reliable catalog identity and champion availability/ownership/queue eligibility, pick/ban filtering, observation ordering, freshness, loss/recovery and retired/new-session behavior. Identify unsupported or unobservable cases and safe collecting/unavailable behavior; do not assume a native durable revision. |
| Trustworthy sources | Identify a small defensible set of role-frequency, recommendation, rune and champion-catalog sources. Compare patch, queue and rank coverage, provenance, sample limitations and update cadence. Record access requirements, licensing/redistribution terms, cost, rate limits and privacy constraints; document gaps rather than inventing evidence. |
| Policy and access | Recheck current Riot policies, product/endpoint registration requirements and supported access boundaries for LCU, recommendations and eventual overlay delivery. Cite dated official evidence and unresolved questions; product approval is not Riot approval. |
| Score design | Propose an explainable design on the approved inclusive 5.0..0.0 scale, with higher-is-stronger meaning, descending display, readable reasons and mains preference without excluding eligible non-mains. Assess candidate approaches and evidence needs, including missing-data behavior. Do not select or claim a validated formula, weights, thresholds, calibrated confidence, win probability or score-to-empirical-efficacy mapping now. Missing evidence must not become score 0.0. |

## Report and acceptance

- [ ] State a go/no-go/conditional conclusion for the approved contract, supported by dated references and an evidence table distinguishing observed, documented, inferred and unknown claims. Explain conditions, blockers, coverage gaps and known limits; do not infer universal feasibility from one client/queue observation.
- [ ] Include a source comparison with patch/queue/rank coverage, update cadence and access/licensing/cost/privacy constraints, plus policy/registration findings. Keep private identifiers, credentials and raw player payloads out of public artifacts; any future collection needs a separately approved, minimized plan.
- [ ] Present explainable scoring design options and mains-weighting tradeoffs for a later decision. Identify which evidence would justify formula selection and which separate study would be needed for efficacy claims; this report must not turn synthetic fixture values into champion evaluations.
- [ ] Derive only bounded implementation issues justified by findings, with dependencies, explicit non-goals and acceptance criteria. Reuse [#7](https://github.com/Eduvent/riftwise/issues/7) for live runes and [#5](https://github.com/Eduvent/riftwise/issues/5) for overlay delivery; do not duplicate those scopes.
- [ ] Provide an automated validation plan for scoring bounds/order/reasons, mains/non-main eligibility, picks/bans, role unknowns/swaps, availability and observation/freshness/session races. Use the approved timelines as acceptance oracles, not proof of an existing engine.
- [ ] Provide a separately authorized real-client validation plan specifying supported queues/client versions, observations to collect, privacy safeguards and pass/fail evidence for roles/swaps, availability, ordering, freshness, recovery and display integration. Record untestable cases and stop conditions rather than promising detection of informal agreements.

## Boundaries and references

Research may recommend narrowing or sequencing future implementation, but cannot silently redefine #4's approved product behavior. No engine, API, overlay, automatic rune application, gameplay automation or broad data collection is authorized by this scope. Existing catalog identity/snapshot methods remain reuse references, not proof of Draft suitability.

- Approved definition: [live Draft contract](https://github.com/Eduvent/riftwise/blob/main/docs/live-draft-contract.md) and [synthetic fixtures](https://github.com/Eduvent/riftwise/blob/main/docs/draft-fixtures.json).
- Separate enhancement boundaries: [rune scope](https://github.com/Eduvent/riftwise/blob/main/docs/rune-suggestions-follow-up.md) / #7; overlay / #5.
- Starting policy reference to recheck: [official League Client API documentation](https://developer.riotgames.com/docs/lol#league-client-api). It is not evidence that registration or product approval has already been obtained.
