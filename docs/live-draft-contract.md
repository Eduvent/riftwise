# Automatic live Draft assistance — approved product contract

**Goal:** champion select automatically produces refreshed, scored champion recommendations with reasons on a second screen or overlay. The player does not request advice, enter a role each match, or supply a candidate list. **Product behavior and synthetic fixtures are approved with the numeric-scale correction below.** Issue #4 defines the product; it does not implement an engine or approve a ranking formula, data sources, empirical efficacy, LCU behavior or policy compliance.

## Happy path

1. The player enters champion select. A future local observer reads the draft and identifies the local player's assigned role when available.
2. Internal evaluation combines the observed picks/bans, catalog-backed availability, role context and optional saved mains. For example, saved middle mains Ahri/Vex receive preference without excluding Annie or other eligible non-mains.
3. A second-screen view (two monitors) or overlay (one monitor) refreshes recommendations, numeric ratings and reasons automatically as the draft changes. Show cards in descending score order. Multiple useful options are the goal, not a notes-only alphabetical catalog. The ranking formula, data sources and empirical efficacy still need separate review.
4. A newly observed role assignment updates the advice. Losing fresh state pauses it; ending the draft clears it. No stale recommendation carries into a new session.

No HTTP endpoint, CLI change, ranking algorithm or runtime UI is designed or implemented here. The existing manual item/recipe planner is independent; its form, scores and confidence are not Draft inputs or selection evidence.

### What the player sees

| State | Meaning and presentation |
| --- | --- |
| `waiting` | No active draft; “Waiting for champion select.” No old cards. |
| `collecting` | Coherent draft, but role or recommendation context is missing. Explain what is being awaited; do not ask for a per-match form. No unsupported role-specific advice. |
| `live` | Current, coherent evaluation; show available champion options, numeric scores and readable reasons in descending score order. Prefer multiple options when supported; one remaining option is not padded with invented alternatives. |
| `unavailable` | Connection loss, stale, inconsistent or out-of-order state; clear actionable cards and explain reconnection/resynchronization. Never blame “invalid input” on the player. |

Detected local roles and **probable** hidden enemy lanes have different labels. Do not show a match-outcome probability or invented confidence/performance. Cards need not disclose every source; internal source/version/patch metadata remains available for debugging. Missing role data is not a player error. “Detected” means reported client assignment, not proof of an informal chat agreement.

### Pick rating scale

Every displayed suggested pick has a finite numeric `score` within **0.0..5.0 inclusive**. Higher means a stronger internal evaluation: **5.0 is highest; 0.0 is lowest**. Display cards in descending (non-increasing) score order. Scores are internal valuations, not probabilities, calibrated confidence or a mapping to measured gameplay efficacy. No formula, factors/weights, thresholds or tie policy is selected here.

Missing context or evidence is **not score 0.0**. Keep actionable cards empty while collecting required context or while unavailable; a numeric minimum cannot substitute for an unsupported evaluation. The existing waiting, unknown-role and freshness rules still apply.

### Concrete synthetic example

The following summary is pinned to `automatic_mains_and_draft_updates/detected_middle` in the [fixtures](draft-fixtures.json); the fixture stores the complete evaluation and presentation. Scores and their descending order are authored solely to test the contract, **not actual champion evaluations**. Other existing steps include score 0.0 to exercise the lower boundary; it is not an unknown-data sentinel.

```json
{
  "case": "automatic_mains_and_draft_updates",
  "step": "detected_middle",
  "state": "live",
  "role_label": "Middle · detected",
  "recommendations": [
    {"champion": "Vex", "score": 5.0, "reasons": ["Middle option; saved middle main."]},
    {"champion": "Ahri", "score": 4.0, "reasons": ["Middle option; saved middle main."]},
    {"champion": "Annie", "score": 3.0, "reasons": ["Middle option outside your mains."]}
  ]
}
```

Lux and Senna have probable lane alternatives in that step. Their feasible pairings retain Lux middle/Senna bottom, Lux middle/Senna support, and Lux support/Senna bottom; they do not assign both champions to support. Later picks/bans remove Vex and Ahri respectively without removing the non-main Annie.

## Internal producer → evaluator → presentation contract

This is an internal responsibility boundary, not a public API or a new event framework. Fixture names are a small notation for hypothetical observations; they are not proposed LCU wire messages.

| Responsibility | Required facts and limits |
| --- | --- |
| Observer/normalizer | Active draft/session identity, locally ordered observation revision, freshness/connection state, local cell and observable role assignment, allied/enemy picks and bans, catalog-backed availability. Retain unknowns rather than fabricate facts. No hidden player identification. |
| Evaluation context | One immutable catalog snapshot and compatible versioned role/recommendation context; optional separately persisted mains preferences. Availability/ownership/queue eligibility must come from supported observations or explicit integration prerequisites, never from the mains list. |
| Evaluator | Bind results to the same session, observation, catalog/context and preference versions. Recompute or invalidate on relevant pick/ban, role, availability, context or preference changes. Remove unavailable champions; do not reject the whole draft because a former option was picked/banned. |
| Consumer | Render only a result matching the current valid binding. Refresh automatically; clear prior advice before awaiting a replacement. Keep waiting/collecting/live/unavailable separate from internal validity reasons. |

### Catalog pool and preferences are different

The pool comes from catalog identity plus observed availability; it is not a user-supplied list and has no arbitrary 1–5 candidate input limit. Mains are optional per-role preferences, potentially persisted by a separately scoped settings capability. This task creates no preference storage or settings API. Missing preferences mean no personalization, not no candidates. Removed/unresolved saved mains cannot become available through preference. Unknown local roles cannot be filled from a mains list.

Reuse `catalog.ResolveChampion` in `internal/catalog/identity.go` for identity: trimmed exact map key, then sorted-key case-insensitive key/ID/name match, then sorted-key punctuation/spacing-insensitive match. Do not add a second alias resolver or assume numeric IDs resolve unless the catalog supports them. Reuse isolated `catalog.Manager.Current`/`catalog.Clone` snapshots; canonical roles are `top`, `jungle`, `middle`, `bottom`, `support`, with an explicit unknown wrapper. An adapter must map supported client values without changing shared role semantics. Unresolved observed identities make affected advice unavailable/collecting, not a guessed champion.

The synthetic pool deliberately has seven available champions. Its `eligible` list means available and not picked/banned, **not** proof every option fits the local role. Displayed options are authored examples drawn from that list. A future evaluator must separately justify role/context suitability. Do not repurpose item scores, item profile confidence, damage priors or historical outcome aggregates as champion-pick evidence.

### Role detection and swap feasibility

Community examples of local LCU `GET /lol-champ-select/v1/session` expose `localPlayerCellId`, `myTeam[].cellId` and `assignedPosition`, including blank assignments. A future adapter should match the local cell, read its assignment, and retain blank/missing/unsupported values as unknown. See the [community LCU reference](https://lcu.vivide.re/), which lists position-swaps separately from pick-order-swaps. These are feasibility clues supplied for this proposal, not current-client proof.

- An **observable assignment update** after an explicit position swap should automatically replace the role and recompute recommendations. Whether and how the current client exposes that update requires real-client validation.
- A pick-order swap changes selection order, not necessarily lane. A champion trade likewise does not prove a role swap. Do not infer a changed role from either alone.
- An informal chat agreement may never update observable assignment. Automatic detection cannot be guaranteed; advice continues to follow the reported assignment. No chat parsing or per-match role prompt is added here.

Riot's [official League Client API documentation](https://developer.riotgames.com/docs/lol#league-client-api) describes LCU as unsupported and asks developers to register endpoint usage. Before integration, recheck policy and registration requirements, current endpoint behavior and safe access. This is neither blanket permission nor an invented blanket prohibition on recommendations. No LCU integration, endpoint access or policy approval is claimed by #4's artifacts.

### Enemy lanes and future analysis

Use observed champions plus trusted curated/statistical **per-role frequency context**, retaining internal source, dataset/rule version, applicable patch and limitations. Hidden lanes remain probable even when one looks likely. Preserve flex alternatives and feasible team assignments; do not independently force two champions into one lane. If supporting context is missing, retain “Lane unknown” rather than invent frequencies or identify hidden players. Algorithm and data-source selection are future scope; fixtures use authored role alternatives, not measured statistics.

Long-term analysis of **synergy, counters and AD/AP composition** remains a product roadmap, not a notes-only ceiling and not required runtime work here. Empirical efficacy, ranking/formula acceptance, population/patch applicability and calibration need independent evidence and approval. The first [Draft feasibility and scoring investigation](draft-engine-investigation.md) separates that research from #4's approved product definition. [Live rune suggestions #7](https://github.com/Eduvent/riftwise/issues/7) are a separate enhancement ([scope](rune-suggestions-follow-up.md)); existing curated manual pregame rune copy is not that feature.

### Freshness and revision rules

Revisions in fixtures are synthetic local ordering markers, not claims that LCU supplies a durable event sequence. An implementation must establish observation order and a freshness budget during integration; no polling interval or stale-time performance claim is approved here.

- A fresh coherent observation binds an evaluation to one active session/revision and compatible catalog/context/preferences. An older in-flight evaluation cannot publish after a newer relevant observation.
- On connection loss or expiry of the future freshness budget, invalidate the binding and remove actionable advice. Reconnection alone is insufficient: accept a fresh coherent read and evaluate it before returning to live.
- A same-session older revision or inconsistent snapshot (for example, Ahri both picked and banned) must not render current advice. Pause and resynchronize; the accepted high-water revision cannot move backward. A rejected observation is not an accepted revision. Exact duplicate observations may be ignored, never replayed as new advice.
- A draft end retires that session and clears advice, pending work and role context. A new session starts clean and may restart its local revision at one. Discard retired-session observations/results without replacing the new session's state.

These are safety invariants, not a storage/event-bus protocol. Internal diagnostic validity values (`current`, `no_session`, `disconnected`, `stale`, `out_of_order`, `inconsistent`) are not user-facing error codes. When binding is invalid, the fixture's evaluation role is unknown, eligibility is empty and cards are cleared; this does not erase saved preferences or the catalog.

## Acceptance fixtures and passive inspection

[draft-fixtures.json](draft-fixtures.json) stores four independent cases with ordered steps and **complete** expected evaluation/presentation objects, not patches or prose-only assertions. Each fresh observation is a complete synthetic normalized snapshot, not an incremental LCU event. `cue` explains an observed swap category; informal chat is deliberately absent from observable fields. The first case accumulates picks/bans. Each case starts independently; no persistent player data is captured.

| Case | Coverage |
| --- | --- |
| `automatic_mains_and_draft_updates` | Waiting, detected middle, Ahri/Vex preferences with Annie eligible, probable flex lanes, new pick and ban recomputation. |
| `observable_and_unobservable_swaps` | Assigned role, pick-order/champion non-proof, observable position update, unobservable chat-swap limitation. |
| `unknown_or_missing_role` | Blank and absent assignment; collecting without a form or invented role. |
| `freshness_and_session_boundaries` | Loss, stale, recovery, older revision, conflicting state, fresh recovery, end, new session, retired-session arrival. |

Patch `0.0.0`, versions and revisions are synthetic identifiers/order markers. Card scores are synthetic numeric valuations, authored only to exercise the inclusive scale and descending order, not actual champion evaluations. The role pair scenarios and card copy are hypothetical acceptance choices, not gameplay evidence. No Draft engine has executed these cases. Applicable behavior RED/GREEN and differential runtime checks are inapplicable: only passive artifacts change, and no shared runtime helpers change. Do not implement a validator/model merely to execute them. No Go/Node suites are required.

Run from the repository root; the standard-library command reads only these artifacts, writes nothing and performs **stored-artifact consistency inspection**, not engine evaluation:

```sh
python3 -c 'from pathlib import Path; exec(Path("docs/live-draft-contract.md").read_text().split("```python\n", 1)[1].split("\n```", 1)[0])'
```

```python
import json
import math
import re
from pathlib import Path


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        assert key not in result, f"Duplicate key: {key}"
        result[key] = value
    return result


data = json.loads(Path("docs/draft-fixtures.json").read_text(), object_pairs_hook=unique_object)
assert data["version"] == "live-draft-fixtures.v1"
cat, ctx, prefs = (data[k] for k in ("catalog", "context", "preferences"))
assert ctx["catalog_ref"] == cat["id"] and ctx["patch"] == cat["patch"] == "0.0.0"
assert all(bundle[k] for bundle in (cat, ctx) for k in ("id", "source", "version", "patch"))
champions = cat["champions"]
assert len(champions) == len(set(champions)) and len(cat["available"]) > 5
assert set(cat["available"]) <= set(champions)
assert prefs["mains"] == {"middle": ["Ahri", "Vex"]}
roles = {"top", "jungle", "middle", "bottom", "support"}
for champ, alternatives in ctx["role_context"].items():
    assert champ in champions and len(set(alternatives)) == len(alternatives)
    assert set(alternatives) <= roles
assert ctx["feasible_pair_scenarios"] == [
    {"Lux": "middle", "Senna": "bottom"},
    {"Lux": "middle", "Senna": "support"},
    {"Lux": "support", "Senna": "bottom"},
]
for scenario in ctx["feasible_pair_scenarios"]:
    assert len(set(scenario.values())) == len(scenario)
    assert all(role in ctx["role_context"][champ] for champ, role in scenario.items())
required = {
    "automatic_mains_and_draft_updates": ["waiting", "detected_middle", "enemy_picks_vex", "ahri_banned"],
    "observable_and_unobservable_swaps": ["before_swaps", "pick_order_only", "champion_swap_only", "explicit_position_update", "informal_swap_unobservable"],
    "unknown_or_missing_role": ["blank_assignment", "missing_assignment"],
    "freshness_and_session_boundaries": ["initial_live", "client_loss", "stale", "recovery", "late_revision", "contradictory_snapshot", "coherent_recovery", "draft_ended", "new_session", "retired_session_arrival"],
}
cases = {case["id"]: case for case in data["cases"]}
assert len(cases) == len(data["cases"]) and set(cases) == set(required)
outputs = {}
observed_scores = []
# Authored card identity/reason expectations, not a recommendation algorithm.
card_reasons = {
    ("middle", "Vex"): ["Middle option; saved middle main."],
    ("middle", "Ahri"): ["Middle option; saved middle main."],
    ("middle", "Annie"): ["Middle option outside your mains."],
    ("middle", "Lux"): ["Flexible middle option outside your mains."],
    ("support", "Senna"): ["Support option; no saved support mains."],
    ("support", "Lux"): ["Flexible support option; no saved support mains."],
}
for cid, case in cases.items():
    assert case["preference_ref"] == prefs["id"]
    assert [s["id"] for s in case["steps"]] == required[cid]
    snapshots = {(s["event"]["session"], s["event"]["revision"]): s["event"]
                 for s in case["steps"] if s["event"]["kind"] == "fresh_snapshot"}
    for step in case["steps"]:
        event, expected = step["event"], step["expected"]
        assert set(expected) == {"evaluation", "presentation"}
        ev, view = expected["evaluation"], expected["presentation"]
        assert set(ev) == {"validity", "binding", "role", "eligible", "context_ref"}
        assert set(ev["role"]) == {"value", "basis"} and ev["context_ref"] == ctx["id"]
        assert set(view) == {"state", "message", "role_label", "enemy_lanes", "recommendations", "notices"}
        assert view["message"] and all(isinstance(view[k], list) for k in ("enemy_lanes", "recommendations", "notices"))
        assert len(ev["eligible"]) == len(set(ev["eligible"]))
        if event["kind"] == "fresh_snapshot":
            assert all(set(event[k]) <= set(champions) for k in ("allied_picks", "enemy_picks", "bans"))
        if ev["validity"] == "current":
            assert set(ev["binding"]) == {"session", "revision"}
            bound = snapshots[(ev["binding"]["session"], ev["binding"]["revision"])]
            occupied = bound["allied_picks"] + bound["enemy_picks"] + bound["bans"]
            assert len(occupied) == len(set(occupied))
            assert ev["eligible"] == [c for c in cat["available"] if c not in occupied]
            role = bound.get("local_role") or "unknown"
            assert ev["role"] == {"value": role, "basis": "detected" if role != "unknown" else "unavailable"}
            assert view["state"] == ("live" if role != "unknown" else "collecting")
            assert view["role_label"] == (role.capitalize() + " · detected" if role != "unknown" else "Role not available")
            assert [lane["champion"] for lane in view["enemy_lanes"]] == bound["enemy_picks"]
        else:
            assert ev["validity"] in {"no_session", "disconnected", "stale", "out_of_order", "inconsistent"}
            assert ev["binding"] is None and ev["eligible"] == []
            assert ev["role"] == {"value": "unknown", "basis": "unavailable"}
            assert view["state"] == ("waiting" if ev["validity"] == "no_session" else "unavailable")
            assert view["role_label"] == "Role not available" and not view["enemy_lanes"]
        cards = view["recommendations"]
        assert len({card["champion"] for card in cards}) == len(cards)
        assert bool(cards) == (view["state"] == "live")
        for card in cards:
            assert set(card) == {"champion", "score", "reasons"} and card["champion"] in ev["eligible"]
            score = card["score"]
            assert type(score) in (int, float) and math.isfinite(score)
            assert 0.0 <= score <= 5.0
            assert card["reasons"] == card_reasons[(ev["role"]["value"], card["champion"])]
            observed_scores.append(score)
        assert all(a["score"] >= b["score"] for a, b in zip(cards, cards[1:]))
        for lane in view["enemy_lanes"]:
            assert set(lane) == {"champion", "label", "roles", "basis"}
            alternatives = ctx["role_context"].get(lane["champion"], [])
            assert lane["roles"] == alternatives
            assert lane["basis"] == ("probable" if alternatives else "unknown")
            assert lane["label"] == ("Probable: " + " or ".join(alternatives) if alternatives else "Lane unknown")
        outputs[(cid, step["id"])] = expected
assert len(outputs) == 21 and {0.0, 5.0} <= set(observed_scores)
first = "automatic_mains_and_draft_updates"
initial = outputs[(first, "detected_middle")]
assert "Annie" in initial["evaluation"]["eligible"] and "Annie" not in prefs["mains"]["middle"]
assert [c["champion"] for c in initial["presentation"]["recommendations"]] == ["Vex", "Ahri", "Annie"]
assert [c["champion"] for c in outputs[(first, "enemy_picks_vex")]["presentation"]["recommendations"]] == ["Ahri", "Annie"]
assert [c["champion"] for c in outputs[(first, "ahri_banned")]["presentation"]["recommendations"]] == ["Annie"]
swap = "observable_and_unobservable_swaps"
assert outputs[(swap, "before_swaps")]["presentation"] == outputs[(swap, "pick_order_only")]["presentation"] == outputs[(swap, "champion_swap_only")]["presentation"]
assert outputs[(swap, "explicit_position_update")]["presentation"] == outputs[(swap, "informal_swap_unobservable")]["presentation"]
assert outputs[(swap, "explicit_position_update")]["evaluation"]["role"]["value"] == "support"
assert [c["champion"] for c in outputs[(swap, "before_swaps")]["presentation"]["recommendations"]] == ["Vex", "Ahri", "Annie"]
assert [c["champion"] for c in outputs[(swap, "explicit_position_update")]["presentation"]["recommendations"]] == ["Senna", "Lux"]
life = "freshness_and_session_boundaries"
assert [c["champion"] for c in outputs[(life, "initial_live")]["presentation"]["recommendations"]] == ["Vex", "Ahri", "Annie"]
for sid in ("recovery", "coherent_recovery"):
    assert [c["champion"] for c in outputs[(life, sid)]["presentation"]["recommendations"]] == ["Ahri", "Annie", "Lux"]
assert [outputs[(life, sid)]["evaluation"]["validity"] for sid in required[life]] == [
    "current", "disconnected", "stale", "current", "out_of_order", "inconsistent", "current", "no_session", "current", "current"
]
assert outputs[(life, "retired_session_arrival")] == outputs[(life, "new_session")]
text = Path("docs/live-draft-contract.md").read_text()
example, = [json.loads(b, object_pairs_hook=unique_object) for b in re.findall(r"```json\n(.*?)\n```", text, re.S)]
view = outputs[(example["case"], example["step"])]["presentation"]
assert example == {"case": first, "step": "detected_middle", "state": view["state"], "role_label": view["role_label"], "recommendations": view["recommendations"]}
print(f"PASS: {len(cases)} cases, {len(outputs)} complete stored outputs; {len(observed_scores)} cards with finite 0.0..5.0 scores in descending order, both boundaries observed; identities/reasons, references, timelines, preferences, probable/detected labels and concrete example consistent. No Draft engine executed.")
```

## Review and next steps

**Approved definition:** product behavior and synthetic fixtures, including every pick's 5.0-to-0.0 rating and descending display order. This is sufficient for #4's product-definition acceptance; research and runtime delivery are separate, not remaining completion criteria for this definition issue.

Authored: this live contract, [superseded pointer](manual-draft-contract.md), synthetic timelines, [#4 issue body](issue-4-live-draft.md), [first investigation scope](draft-engine-investigation.md), [rune follow-up](rune-suggestions-follow-up.md) and README summary. Integration, issue publication and closure belong to the parent workflow; these files do not claim they have occurred.

**Future gates:** separately approve implementation, ranking/evidence criteria and source selection. Obtain policy/endpoint-usage evidence, real-client assigned-role and explicit-swap observations, unknown-role behavior, availability, ordering/freshness/recovery, and second-screen/overlay validation. The investigation proposes a bounded path to those decisions; neither its publication nor passive fixture inspection proves them.
