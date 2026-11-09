# Manual champion Draft v1 — proposed contract, not an implemented API

**Proposal for issue #4:** compare a small, manually supplied set of legal next allied picks, without ranking them. Bans are exclusions, not suggested actions. Neither this document nor its fixtures approve a ranking model or claim strategic efficacy. `/api/pregame/plan` remains an item/recipe planner; **no Draft endpoint or runtime behavior is implemented**.

## Review path

1. Read the request and validation rules below.
2. Review the [pinned synthetic fixtures](draft-fixtures.json), especially the accepted response in `partial_unknown_roles` and the rejection cases.
3. Review evidence boundaries and infrastructure reuse. Human contract/fixture review and any future ranking-model approval remain **pending**.

This v1 is a pure proposed input/output contract, not an HTTP design. HTTP routes, status codes, body-size limits, UI, automatic capture, persistence, data collection, model training and production catalog/evidence loading are outside this unit. No player identities, live snapshots, item inventories, gold, scores or preference history are inputs.

## Request: `manual-draft.v1`

Exactly one JSON object is considered. Object key order is irrelevant. Unknown fields, duplicate object keys, trailing JSON, wrong types and explicit nulls are invalid unless allowed below. No coercion occurs. Arrays are required even when empty. All enums are case-sensitive, exact strings.

| Field | Required | Shape and bounds |
| --- | --- | --- |
| `version` | Yes | Exactly `"manual-draft.v1"`. |
| `intent` | Yes | Exactly `"pick"`. Ban recommendations are unsupported. |
| `allied`, `enemy` | Yes | Objects containing only required `picks` and `bans` arrays. |
| `allied.picks` | Yes | 0–4 pick objects, leaving a next allied slot. A completed allied team is rejected, not interpreted as a replacement request. |
| `enemy.picks` | Yes | 0–5 pick objects. |
| `allied.bans`, `enemy.bans` | Yes | Each 0–5 champion strings. Empty means no bans supplied, not proof no other bans exist. |
| `candidates` | Yes | 1–5 champion strings supplied by the caller; no implicit pool, truncation or fallback. One candidate is valid but cannot form a comparison. |
| `target_role` | No | Canonical role, `"unknown"`, null or omitted. Null/omitted normalize to `"unknown"`. This is selection intent, not an assignment to an existing pick. |

A pick contains required `champion` and optional `role`, `flex_roles`, with no other fields:

| Field | Semantics |
| --- | --- |
| `champion` | Nonblank string, at most 128 UTF-8 bytes before trimming; resolve against the pinned catalog. |
| `role` | `top`, `jungle`, `middle`, `bottom`, `support`, or `unknown`. Null/omitted normalize to `unknown`; empty strings and aliases such as `mid` are invalid. |
| `flex_roles` | Omitted normalizes to `[]`. Otherwise an array of 2–5 distinct canonical roles, allowed only with an unknown role. Null, `[]`, one role, duplicates or `unknown` inside the array are invalid. This is user-stated ambiguity, not an inferred role distribution. |

Champion strings in bans and candidates have the same identity and byte bounds as `champion`. Reuse **`catalog.ResolveChampion`** (`internal/catalog/identity.go`): trimmed exact map key first, then sorted-key case-insensitive key/ID/name lookup, then sorted-key punctuation/spacing-insensitive lookup. Never add a second alias policy, resolve numeric Data Dragon keys unless the catalog already exposes them, or silently substitute an unknown champion. Catalog keys and canonical IDs in the fixture snapshot coincide.

### Roles and partial information

- Known roles must be unique within each team's picks. The same role on opposing teams is legal. A known allied role cannot also be the explicit `target_role`.
- Unknown picks are retained. Missing picks, bans and roles are not filled from popularity, champion tags, item profiles, the other team or live state.
- Flex sets may overlap other flex sets, known roles and `target_role`. Preserve that ambiguity even if a unique or impossible complete assignment seems derivable; v1 does not solve team-role feasibility or infer a lane opponent. Only duplicate **explicit known** assignments are rejected.
- Candidate legality means resolved, distinct and absent from all supplied picks/bans. It does not certify role suitability, ownership, queue eligibility, turn order or complete-draft feasibility. An unknown target role does not make a legal candidate invalid.
- Incomplete context does not alone block candidate-only structural annotations. It cannot justify role-specific, matchup or team-synergy claims. This v1 only permits unconditional per-candidate structural notes; contextual rules need a separately reviewed extension.

## Validation and deterministic errors

Validation is atomic: a rejected request has no normalized draft, alternatives, evidence or uncertainty and changes no state. There is no partial acceptance or silent deduplication. Missing evidence is **not** invalid input.

Apply these stages in order; stop at the first failing stage. Shape checks descend only through correctly typed parents. Return exactly one error: the lexicographically smallest `(path, code)` among that stage's errors. Paths are RFC 6901 JSON Pointers into the original input, with zero-based array indices and `""` for the document root. For duplicates/conflicts, point to the later occurrence in traversal order: allied picks, enemy picks, allied bans, enemy bans, candidates; each array in input order. Duplicated roles point to the later pick's `/role`. Flex-set ambiguity alone is not a conflict.

| Stage | Checks | Error codes (exact) |
| --- | --- | --- |
| 1. Shape | Required members, allowed members, JSON types, null exceptions, champion string bounds; malformed/trailing JSON or duplicate object keys use root path. Enum strings and array lengths are checked later. | `required`, `unknown_field`, `invalid_type`, `invalid_identity`, `invalid_json` |
| 2. Version and enums | Exact version/intent; canonical roles; flex rules above. | `unsupported_version`, `unsupported_intent`, `invalid_role`, `invalid_flex_roles` |
| 3. Cardinality | All pick/ban/candidate bounds. | `cardinality` |
| 4. Identity | Every champion resolves in the same immutable snapshot. | `unknown_champion` |
| 5. Consistency | A champion may appear only once across all supplied picks/bans (`champion_conflict`), including repeated bans across teams. Candidate duplicates use `duplicate_candidate`; candidates present in picks/bans use `candidate_unavailable`. Explicit known roles are unique per team; target role is not occupied by an explicit allied role. | `champion_conflict`, `duplicate_candidate`, `candidate_unavailable`, `duplicate_role`, `target_role_occupied` |

Within stage 5 a candidate already present in picks/bans reports `candidate_unavailable`; otherwise later repetitions report `duplicate_candidate`. Compare identities **after** resolution, including aliases. Reject the whole request if even one candidate is unavailable; do not quietly filter it. A malformed individual flex array reports at `/flex_roles`, not at an element. All other enum/identity errors point to the offending scalar. Errors have only `code` and `path`; there is no free-form error message in v1.

## Normalized response

Every response has exactly these fields; arrays never become null:

| Field | Shape |
| --- | --- |
| `version` | Always `"manual-draft.v1"`, including unsupported-request-version rejections. |
| `decision` | `accept` or `reject`. |
| `status` | `comparison_available`, `insufficient_evidence`, or `invalid_input`. |
| `normalized` | Accepted: complete normalized request. Rejected: null. |
| `alternatives` | Accepted: one entry for every candidate. Rejected: `[]`. |
| `evidence` | Accepted: provenance/reference object below. Rejected: null. |
| `uncertainty` | Accepted: object below. Rejected: null. |
| `errors` | Accepted: `[]`. Rejected: exactly one `{code, path}`. |

The normalized shape is output, not a resubmission format: in particular, its default `flex_roles: []` must be omitted if resubmitted. Normalization preserves pick order (no slot/turn inference); expands each pick to `{champion, role, flex_roles}`; adds `target_role`; replaces all champion strings with canonical IDs; sorts bans and candidates by canonical ID, ascending bytewise; and sorts flex roles in `top, jungle, middle, bottom, support` order. Version, intent and both teams remain present. No fields are otherwise added to the normalized request.

Each alternative is exactly:

```json
{"champion_id":"Ahri","champion_name":"Ahri","reason_codes":["curated_structure"],"evidence_ids":["s-ahri"]}
```

`reason_codes` is `["curated_structure"]` when at least one applicable structural record exists, otherwise `["structural_evidence_missing"]` with no evidence IDs. These are annotation availability reasons, **not** suitability endorsements. All applicable record IDs are included, sorted ascending. Evidence text and limitations are obtained from the referenced pinned records, not invented by the consumer. Alternatives sort by champion ID, never evidence count or perceived strength. No primary choice, rank, score, win probability or numeric/calibrated confidence is allowed. No suggested bans are returned.

Accepted `evidence` is exactly `{catalog_id, structural_set_id, structural_ids, empirical_ids}`. IDs select immutable, versioned bundles in the supplied evaluation context; `structural_ids` is the sorted union of alternative evidence IDs; `empirical_ids` is always `[]` in v1. Referenced records and bundle provenance must travel with the review artifact so an ID alone cannot masquerade as evidence. The fixture file pins every bundle and record; they are not fetched remotely or supplied by a request. Any future transport must expose those records and provenance, not only opaque references. No production evidence loader is specified here.

Accepted `uncertainty` is exactly:

- `unknown_role_paths`: lexicographically sorted pointers into the normalized request for every unknown pick role and unknown target role (also includes unknown roles with flex sets).
- `flex_role_paths`: lexicographically sorted pointers to nonempty normalized pick flex arrays; no probabilities or chosen assignment.
- `missing_structural_ids`: sorted candidate IDs lacking applicable structural notes.
- `comparison_limits`: sorted codes. Always includes `empirical_evidence_unavailable`; adds `single_candidate` for one candidate and `structural_evidence_missing` when any candidate lacks a structural note.

`comparison_available` means **at least two candidates and a structural note for every candidate**, not a recommendation or evidence of superior outcomes. Otherwise accept with `insufficient_evidence`, retain every legal alternative and available note, and name the missing evidence in uncertainty. Empirical absence always stays explicit but does not block this bounded structural comparison. Missing context is never converted into fabricated facts or zero-probability outcomes.

### Concrete accepted exchange

Input (the fixture `partial_unknown_roles`):

```json
{
  "version": "manual-draft.v1", "intent": "pick",
  "allied": {"picks": [{"champion": "Jinx"}], "bans": []},
  "enemy": {"picks": [{"champion": "Kha'Zix", "role": null}], "bans": ["Leona"]},
  "candidates": ["garen", " AHRI "], "target_role": null
}
```

Exact response under `synthetic-catalog.v1` and `synthetic-structure.v1`:

```json
{
  "version": "manual-draft.v1", "decision": "accept", "status": "comparison_available",
  "normalized": {
    "version": "manual-draft.v1", "intent": "pick",
    "allied": {"picks": [{"champion": "Jinx", "role": "unknown", "flex_roles": []}], "bans": []},
    "enemy": {"picks": [{"champion": "Khazix", "role": "unknown", "flex_roles": []}], "bans": ["Leona"]},
    "candidates": ["Ahri", "Garen"], "target_role": "unknown"
  },
  "alternatives": [
    {"champion_id": "Ahri", "champion_name": "Ahri", "reason_codes": ["curated_structure"], "evidence_ids": ["s-ahri"]},
    {"champion_id": "Garen", "champion_name": "Garen", "reason_codes": ["curated_structure"], "evidence_ids": ["s-garen"]}
  ],
  "evidence": {"catalog_id": "synthetic-catalog.v1", "structural_set_id": "synthetic-structure.v1", "structural_ids": ["s-ahri", "s-garen"], "empirical_ids": []},
  "uncertainty": {"unknown_role_paths": ["/allied/picks/0/role", "/enemy/picks/0/role", "/target_role"], "flex_role_paths": [], "missing_structural_ids": [], "comparison_limits": ["empirical_evidence_unavailable"]},
  "errors": []
}
```

A rejection example is `{"version":"manual-draft.v1","decision":"reject","status":"invalid_input","normalized":null,"alternatives":[],"evidence":null,"uncertainty":null,"errors":[{"code":"unknown_champion","path":"/candidates/0"}]}`. It conveys invalid input, not an evidence shortage.

## Evidence boundaries

| Kind | What it may establish | Required provenance and limits |
| --- | --- | --- |
| Catalog identity | Canonical ID/name only. | Immutable catalog ID, version/patch and source; absence is not a neutral identity. Catalog tags do not establish strategic efficacy. |
| Curated structural | An explicitly authored, unconditional per-candidate annotation and its limitation. | Stable record ID, kind `curated_structural`, subject `champion_id`, source, revision, exact catalog patch, claim and limitation. A record applies only to that subject and exact patch. No itemization-derived or role-inferred evidence. |
| Empirical (not supported in v1) | Only observed match facts for the documented cohort, never causal advantage by default. | A future contract extension must require dataset/source revision, collection window, exact patches, queue/map/region, population/rank/role definitions and role provenance, sample counts and denominators, outcome definition, filters/exclusions, aggregation method, missingness/bias limitations, and consent/privacy basis. Calibrated confidence/probabilities would additionally require separate validation and approval. |

The synthetic catalog's names/IDs exercise identity resolution; its patch `0.0.0`, structural notes and sources are **synthetic, non-empirical and not gameplay claims**. The two sample notes compare made-up mechanical tradeoffs, not real Ahri/Garen capabilities. The empty structural set is an intentional absence, not an invalid bundle. V1 has no empirical records, match outcomes, win rates, tier labels or calibrated confidence. Structural evidence cannot create these or turn a manual scenario into observed match facts.

Evaluation context is trusted, separately pinned review data, not a user input field. Its bundle IDs are unique; all record subjects resolve; records match the catalog patch and required provenance. A missing/corrupt context is an unavailable evaluator/precondition, not invalid champion input and not permission to invent evidence. Infrastructure failure handling is deferred with transport design; the fixtures assume a valid context. An empty valid structural bundle produces accepted insufficient evidence.

## Infrastructure reuse — identity is not selection evidence

| Reuse or reference | Keep independent |
| --- | --- |
| `catalog.ResolveChampion` identity semantics, including deterministic sorted fallback and punctuation/spacing aliases. | Draft picks/bans, legality conflicts, role ambiguity, comparison eligibility and structural annotations. |
| `catalog.Manager.Current` and `catalog.Clone` isolated snapshots; bind one evaluation to one immutable catalog and evidence bundle. | No live tracker state, personalization, historical outcomes or persistent player data. |
| `domain.Role.Valid` canonical known roles. Draft adds an explicit unknown/flex wrapper; do not change this helper or use `ParseRole` aliases here. | Do not infer roles from champion identity or an item profile. |
| Existing strict validation, deterministic ordering and explicit provenance patterns as design precedents. | `pregame.Normalize`/`Service.Plan` are manual **item** normalization/scoring. `itemization.RankPlanning`, itemrules scores, profile confidence, damage priors and item personalization are not champion-selection evidence. |

No shared code is changed or proposed for extraction in this unit. A Draft implementation must be reviewed independently from itemization; perfecting item scores is not a prerequisite to reviewing this contract.

## Fixture review and verification

`draft-fixtures.json` contains a pinned catalog, structural bundles and cases with concrete `input` (or mutually exclusive `raw_input` JSON text for parse failures), context bundle IDs, and an exact complete `expected` response. References resolve only inside that file. `invalid_identity_paths` explicitly marks deliberately unresolved champion inputs; all other champion references must resolve. `identity_examples` pins literal alias pairs for artifact inspection, not a second resolution algorithm. Fixture metadata/descriptions are not request members. The fixture format version is `manual-draft-fixtures.v1`.

| Coverage | Case IDs |
| --- | --- |
| Partial draft, null/omitted/unknown roles, identity aliases, no empirical evidence | `partial_unknown_roles`, `empty_draft_no_empirical`, `explicit_unknown_roles` |
| Flexible ambiguity, overlap, no role solving, explicit roles across teams | `flex_ambiguity`, `explicit_roles`, `occupied_flex_is_preserved` |
| Missing structural notes, no structural bundle records, single candidate | `missing_structural`, `empty_structural`, `single_candidate` |
| Duplicate roles/champions/candidates; cross-team, pick/ban and cross-ban conflicts; unavailable candidates | `duplicate_role`, `duplicate_pick_alias`, `cross_team_pick`, `pick_ban_conflict`, `duplicate_ban`, `cross_team_ban`, `duplicate_candidate`, `candidate_picked`, `candidate_banned`, `target_role_occupied` |
| Unknown identities, bad enums, flex rules, cardinality, schema shape and deterministic precedence | Remaining rejection cases, including `unknown_champion`, `bad_role`, `bad_flex`, `too_many_allied_picks`, `too_many_enemy_picks`, `too_many_bans`, `too_many_enemy_bans`, `too_many_candidates`, `empty_candidates`, `bad_version`, `ban_intent`, `shape_precedes_version`, `lexical_error_order` |
| Legal upper bounds and canonical ordering | `upper_bounds`, `five_candidates` |

These are proposed acceptance oracles, **not tests executed against a Draft engine**. No runnable Draft service exists, so meaningful behavior RED/GREEN is inapplicable. Verification here is JSON parsing, bounded structural/reference/expected-outcome consistency inspection, `git diff --check` and manual contract/example review. Do not implement a validator or model solely to make these fixtures pass. No Go/Node suites are needed for these passive artifacts.

### Reproduce passive artifact checks

Run from the repository root (Python standard library only; reads these two artifacts and writes nothing):

```sh
python3 -c 'from pathlib import Path; exec(Path("docs/manual-draft-contract.md").read_text().split("```python\n", 1)[1].split("\n```", 1)[0])'
git diff --check
```

This bounded inspection checks references, stored expected-outcome consistency and the concrete exchange. It does not validate requests against a Draft implementation or establish that rejections are implemented.

```python
import json
import re
from collections import Counter
from pathlib import Path

def unique_object(pairs):
    result = {}
    for key, value in pairs:
        assert key not in result, f"duplicate artifact key: {key}"
        result[key] = value
    return result

data = json.loads(Path("docs/draft-fixtures.json").read_text(), object_pairs_hook=unique_object)
assert data["version"] == "manual-draft-fixtures.v1"
champions = {c["id"]: c["name"] for c in data["catalog"]["champions"]}
assert len(champions) == len(data["catalog"]["champions"])
sets = {s["id"]: s for s in data["structural_sets"]}
assert len(sets) == len(data["structural_sets"])
assert not data["empirical_records"]
records = {}
for bundle in sets.values():
    assert bundle["patch"] == data["catalog"]["patch"]
    for row in bundle["records"]:
        assert row["id"] not in records and row["champion_id"] in champions
        assert row["kind"] == "curated_structural" and row["patch"] == bundle["patch"]
        assert all(row[k] for k in ("source", "revision", "claim", "limitation"))
        records[row["id"]] = row
aliases = data["identity_examples"]
assert all(value in champions for value in aliases.values())
cases = data["cases"]
assert len({c["id"] for c in cases}) == len(cases)
assert len(cases) <= 100
fields = {"version", "decision", "status", "normalized", "alternatives", "evidence", "uncertainty", "errors"}
for case in cases:
    expected = case["expected"]
    assert set(expected) == fields and expected["version"] == "manual-draft.v1", case["id"]
    assert case["context"]["catalog_id"] == data["catalog"]["id"]
    bundle = sets[case["context"]["structural_set_id"]]
    assert ("input" in case) != ("raw_input" in case)
    marked = set(case.get("invalid_identity_paths", []))
    seen_marked = set()
    def inspect_refs(value, path=""):
        if isinstance(value, dict):
            for key, child in value.items():
                if key == "champion" or key in ("candidates", "bans") and isinstance(child, list):
                    values = [(path + "/" + key, child)] if key == "champion" else [(path + "/" + key + "/" + str(i), v) for i, v in enumerate(child)]
                    for pointer, identity in values:
                        if pointer in marked:
                            assert identity not in champions and identity not in aliases
                            seen_marked.add(pointer)
                        else:
                            assert identity in champions or identity in aliases, (case["id"], pointer)
                else:
                    inspect_refs(child, path + "/" + key)
        elif isinstance(value, list):
            for i, child in enumerate(value):
                inspect_refs(child, path + "/" + str(i))
    if "input" in case:
        inspect_refs(case["input"])
        assert seen_marked == marked
    if expected["decision"] == "reject":
        assert expected["status"] == "invalid_input" and len(expected["errors"]) == 1
        assert all(expected[k] is None for k in ("normalized", "evidence", "uncertainty"))
        assert expected["alternatives"] == []
        error = expected["errors"][0]
        assert set(error) == {"code", "path"} and (error["path"] == "" or error["path"].startswith("/"))
        assert "raw_input" not in case or error == {"code": "invalid_json", "path": ""}
        continue
    assert expected["decision"] == "accept" and not expected["errors"]
    normalized = expected["normalized"]
    inspect_refs(normalized)
    ids = normalized["candidates"]
    assert ids == sorted(set(ids))
    alternatives = expected["alternatives"]
    assert [a["champion_id"] for a in alternatives] == ids
    used, missing = [], []
    for alternative in alternatives:
        champion = alternative["champion_id"]
        assert alternative["champion_name"] == champions[champion]
        refs = alternative["evidence_ids"]
        assert refs == sorted(r["id"] for r in bundle["records"] if r["champion_id"] == champion)
        assert alternative["reason_codes"] == (["curated_structure"] if refs else ["structural_evidence_missing"])
        used.extend(refs)
        if not refs:
            missing.append(champion)
    assert expected["evidence"] == {**case["context"], "structural_ids": sorted(set(used)), "empirical_ids": []}
    uncertainty = expected["uncertainty"]
    assert uncertainty["missing_structural_ids"] == missing
    limits = ["empirical_evidence_unavailable"] + (["single_candidate"] if len(ids) == 1 else []) + (["structural_evidence_missing"] if missing else [])
    assert uncertainty["comparison_limits"] == sorted(limits)
    assert expected["status"] == ("insufficient_evidence" if len(ids) == 1 or missing else "comparison_available")
    for kind, member in (("unknown_role_paths", "role"), ("flex_role_paths", "flex_roles")):
        paths = [f"/{side}/picks/{i}/{member}" for side in ("allied", "enemy") for i, pick in enumerate(normalized[side]["picks"]) if (pick[member] == "unknown" if member == "role" else bool(pick[member]))]
        if member == "role" and normalized["target_role"] == "unknown":
            paths.append("/target_role")
        assert uncertainty[kind] == sorted(paths)
text = Path("docs/manual-draft-contract.md").read_text()
examples = [json.loads(block) for block in re.findall(r"```json\n(.*?)\n```", text, re.S)]
assert examples[1] == cases[0]["input"] and examples[2] == cases[0]["expected"]
counts = Counter(case["expected"]["status"] for case in cases)
print(f"PASS: {len(cases)} unique cases; {len(champions)} pinned champions; {len(records)} structural records; {dict(sorted(counts.items()))}; references, expected outcomes and concrete exchange consistent. No Draft engine executed.")
```

## Human approval gate — still pending

- [ ] Review and approve/narrow the manual input, normalization, validation and partial-information contract.
- [ ] Review exact expected outcomes and synthetic evidence limitations in the fixtures.
- [ ] Separately approve any future implementation scope; define empirical/model acceptance criteria before approving a ranking model.

Self-review of documentation does not satisfy these human approvals. Nothing here authorizes automatic capture, a complete Draft model, LCU integration, storage, overlay UI or a public backend.
