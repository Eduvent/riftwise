# LoL Engine MVP

LoL Engine is a localhost-only Go application that watches the active League of Legends match and presents deterministic, choice-oriented itemization guidance on an embedded second-screen dashboard. A separate manual pregame planner compares a user-supplied scenario without fabricating live telemetry or changing the active match. Garen keeps its pinned v2 route; other champions use strict champion/role profiles with conservative, explicit fallbacks. Recommendations work without AI, remain available from a cached patch catalog, and never control the game.

## Quick start

Requirements: Go 1.26.8 and Internet access for the first Data Dragon synchronization.

```bash
PATH=/opt/homebrew/opt/go@1.26/bin:$PATH \
GOTOOLCHAIN=local \
go run ./cmd/server
```

The server listens on `http://127.0.0.1:8080`. Open that URL in a browser for the live dashboard. It remains available in the `waiting_for_game` state until a compatible match begins.

For a frontend-only preview that never reads or changes backend game state, open:

```text
http://127.0.0.1:8080/?demo=1
```

The preview always carries a visible **MODO DEMO** banner. Normal `/` is always live and never silently substitutes demo data after an API failure.

```bash
curl http://127.0.0.1:8080/api/health
curl http://127.0.0.1:8080/api/dashboard
curl http://127.0.0.1:8080/api/pregame/options
curl -H 'Content-Type: application/json' -d '{"champion":"Garen","role":"top","stage":"first_recall","strategy":"balanced","gold":1200}' http://127.0.0.1:8080/api/pregame/plan
curl -X POST http://127.0.0.1:8080/api/recommendation/recalculate
curl -N http://127.0.0.1:8080/api/events
curl 'http://127.0.0.1:8080/api/matches?limit=20&offset=0'
curl 'http://127.0.0.1:8080/api/matches/{session-id}/decision-events?after=0&limit=50'
curl 'http://127.0.0.1:8080/api/history/matches?limit=20'
curl http://127.0.0.1:8080/api/privacy/history
curl 'http://127.0.0.1:8080/api/history/export?scope=summaries'
curl http://127.0.0.1:8080/api/riot/status
```

## What the MVP does

- Reads `allgamedata` from Riot's loopback Live Client API every two seconds.
- Tracks player identity, position, score, death/respawn state, inventory, gold, game version, map, mode, cumulative Live Client events, and every participant.
- Detects reconnect-safe local match sessions and persists ordered lifecycle, death, and inventory/shop events before publishing them.
- Synchronizes Spanish items and English champion ability markup from Data Dragon.
- Filters known irrelevant Data Dragon placeholders and stores only recipe-closed, complete patch catalogs in SQLite.
- Estimates enemy damage mix, highest threat, and whether Garen is ahead, even, or behind.
- Loads strict patch-versioned Garen v2 rules, ranks a primary plus two or three alternatives, and selects the next affordable recipe component.
- Resolves non-Garen recommendations through `champion+role`, champion default, compatible role archetype, document-priority champion-tag archetype, then a reactive-only neutral fallback.
- Bundles curated structural examples for Garen top, Amumu jungle, Ahri middle, Jinx bottom, and Leona support; they are not win-rate, pick-rate, tier, or meta claims.
- Offers an isolated manual pregame view with catalog-backed champion/item selectors, explicit pressure categories, deterministic item/recipe comparisons, and curated rune, summoner, and initial-plan options.
- Applies optional local personalization with bounded, documented style/item adjustments and opt-in feedback; the default leaves every existing ranking unchanged.
- Stabilizes target changes over time so small score movements do not make the recommendation flicker.
- Persists stable, user-relevant recommendation changes as ordered decision events before publishing them.
- Rebuilds identity-free local match summaries from durable checkpoints and event history, with opt-in retention, safe export, and explicit deletion controls.
- Optionally links one of the user's own Riot accounts and synchronizes privacy-minimized Match-V5 facts for observational historical statistics; this path is disabled by default.
- Streams consistent `dashboard_snapshot` events and additive `decision_notification` hints over SSE.
- Serves an embedded, responsive browser dashboard from the same protected loopback origin.
- Shows accessible text alerts and offers explicitly enabled tone or voice cues that are off and muted by default.
- Recovers automatically from client, Internet, and Data Dragon outages when cached data exists.

## Architecture

```text
Riot Live Client API                 Data Dragon
        |                                |
        v                                v
 Live Client adapter              Catalog synchronizer
        |                                |
        v                                v
 Tracker snapshot store       SQLite complete-patch cache
        |                                |
        +--------------+-----------------+
                       v
             Deterministic analysis
                       |
                       v
       Pinned Garen or generic profiles
             |                    |
             v                    v
 Recommendation coordinator   Pure manual pregame service
             |                    |
             +----------+---------+
                        v
       Deterministic local history builder
                        |
                        v
        HTTP + dashboard SSE + embedded UI
```

| Package | Responsibility |
| --- | --- |
| `internal/liveclient` | Hardened loopback fetch and Riot-to-domain mapping. |
| `internal/tracker`, `internal/state` | Sequential polling, stale recovery, and game subscriptions. |
| `internal/ddragon` | Size-limited, schema-validated Data Dragon HTTP client. |
| `internal/catalog` | Patch catalog types, selection, synchronization, and cache fallback. |
| `internal/storage` | Ordered SQLite schema v1→v10 migrations, metadata, complete catalogs, durable local/optional-Riot checkpoints, materialized summaries, and typed privacy/personalization records. |
| `internal/riotapi` | Secret-redacting Account-V1/Match-V5 direct/backend adapters and privacy-minimized normalization. |
| `internal/matchsync` | Consent, frozen-window synchronization, delayed fetches, restart recovery, and local-session reconciliation. |
| `internal/statistics` | Deterministic observational aggregates and a read-only historical-baseline interface. |
| `internal/history` | Pure deterministic summary construction plus bounded rebuild, keyset pagination, retention, safe export, and deletion coordination. |
| `internal/analysis` | Pure damage, threat, and player-state heuristics. |
| `internal/itemization` | Pure deterministic candidate ranking and recipe engine, with Garen pinned to its existing v2 route. |
| `internal/itemrules` | Strict external JSON rules, patch resolution, catalog validation, and explicit legacy fallback. |
| `internal/itemprofiles` | Separate strict generic profile schema, role/tag fallback resolution, confidence caps, and compatible last-known-good loading. |
| `internal/pregame` | Pure manual-scenario normalization, conservative opponent fill, confidence/provenance, and direct ranker orchestration without live snapshots or persistence. |
| `internal/personalization` | Catalog-validated preference service, immutable scorer snapshots, contextual minimum-sample feedback, bounded effects, reset, and identity-free export. |
| `internal/recommendation` | Automatic/manual recalculation, target stabilization, semantic decision detection, and coherent dashboard revisions. |
| `internal/api` | JSON history endpoints and bounded snapshot/decision SSE delivery. |
| `internal/webui` | Embedded semantic HTML, responsive CSS, safe DOM rendering, accessible decision history, and opt-in local audio. |

SQLite uses the pure-Go `modernc.org/sqlite` driver. Catalog replacement is transactional; startup only loads rows marked complete.

## HTTP API

| Method | Path | Response |
| --- | --- | --- |
| `GET` | `/api/health` | Tracker, catalog, active rule/profile provenance and fallback/error, recommendation readiness, decision-event status/latest event, and dashboard revision. |
| `GET` | `/api/game` | Latest normalized tracker snapshot. |
| `GET` | `/api/recommendation` | Latest itemization result. |
| `GET` | `/api/dashboard` | Atomic game + catalog + recommendation + decision-event availability snapshot. |
| `POST` | `/api/recommendation/recalculate` | Recalculates immediately and returns the result. |
| `GET` | `/api/pregame/options` | Catalog-backed champion/item selectors plus stage, role, strategy, pressure, rule, and profile availability. |
| `POST` | `/api/pregame/plan` | Strict bounded manual scenario; returns normalized inputs, provenance/confidence, ranked item/recipe choices, and curated plan options. |
| `GET` | `/api/personalization/options` | Purchasable map-11 item choices from the active catalog. |
| `GET`, `PUT` | `/api/personalization/preferences` | Reads or strictly replaces style, preferred/avoided items, and feedback opt-in. |
| `POST` | `/api/personalization/preferences/reset` | Restores balanced/default scoring and disables feedback immediately. |
| `GET`, `POST`, `DELETE` | `/api/personalization/feedback` | Aggregate summary; idempotent eligible-choice feedback; or confirmed feedback-only deletion. |
| `GET` | `/api/personalization/export` | Versioned preferences plus aggregate feedback only, without session or decision identifiers. |
| `GET` | `/api/events` | Initial/changed `dashboard_snapshot` events, newly committed `decision_notification` hints, and keepalives. |
| `GET` | `/api/matches?limit=20&offset=0` | Durable newest-first local match history; limits are capped at 100. |
| `GET` | `/api/matches/{id}/events?after=0&limit=50` | Authoritative durable events in ascending per-match sequence order. |
| `GET` | `/api/matches/{id}/decision-events?after=0&limit=50` | Authoritative durable decision events in ascending per-match sequence order; limits are capped at 100. |
| `GET` | `/api/history/matches?limit=20&before={cursor}` | Identity-free, newest-first completed summaries with stable keyset pagination; limits are capped at 100. |
| `GET` | `/api/history/matches/{id}` | One identity-free summary, including an active session when addressed directly; its semantic digest is returned as an `ETag`. |
| `GET` | `/api/history/export?scope=summaries\|full` | Versioned allowlist-only JSON export with `no-store`; `full` adds observed/inferred timelines and decision changes. |
| `DELETE` | `/api/history/matches/{id}` | Deletes one completed, settled local record after the exact `DELETE_LOCAL_MATCH_HISTORY` JSON confirmation; active or still-settling matches are protected. |
| `DELETE` | `/api/history/matches` | Deletes all completed, settled history after the exact `DELETE_LOCAL_HISTORY` confirmation. |
| `GET`, `PUT` | `/api/privacy/history` | Reads or changes optional retention days/maximum completed matches; both are disabled by default. |

Example recommendation shape:

```json
{
  "status": "ready",
  "patch": "16.18.1",
  "player_state": "behind",
  "damage_mix": {"physical": 0.23, "magic": 0.69, "true": 0.08},
  "main_threat": {"champion_name": "Ahri", "score": 8.42},
  "phase": "mid",
  "strategy": "balanced",
  "next_component": {"id": 1057, "name": "Capa negatrón", "cost": 850},
  "target_item": {"id": 4401, "name": "Fuerza de la naturaleza", "cost": 2800},
  "alternative": {"id": 2504, "name": "Rukerno Kaénico", "cost": 2900},
  "alternatives": [
    {"item": {"id": 2504, "name": "Rukerno Kaénico", "cost": 2900}, "score": 790},
    {"item": {"id": 3111, "name": "Botas de Mercurio", "cost": 1250}, "score": 730},
    {"item": {"id": 6631, "name": "Rompeavances", "cost": 3300}, "score": 660}
  ],
  "ranked_options": [
    {"item": {"id": 4401, "name": "Fuerza de la naturaleza", "cost": 2800}, "score": 860},
    {"item": {"id": 2504, "name": "Rukerno Kaénico", "cost": 2900}, "score": 790},
    {"item": {"id": 3111, "name": "Botas de Mercurio", "cost": 1250}, "score": 730},
    {"item": {"id": 6631, "name": "Rompeavances", "cost": 3300}, "score": 660}
  ],
  "rules": {"ready": true, "version": "garen-v2.16.18.1.1", "patch": "16.18.1", "source": "external_exact", "fallback": false, "legacy": false},
  "reason": "Las reglas Garen v2 priorizan resistencia mágica, supervivencia sostenida en la fase mid.",
  "confidence": 0.78,
  "gold_needed": 0
}
```

Recommendation statuses are `waiting`, `stale`, `unsupported`, `catalog_unavailable`, `ready`, `save_for_component`, `inventory_full`, `complete`, `insufficient_context`, and `error`.

Generic results add `profile`, `profile_status`, structured `advisories`, `rune_plan`, `summoner_plan`, and `initial_plan`. The plans are curated structural copy, not live-state inference. `confidence` is capped by the selected profile level and by both match-context and trait confidence. Missing or unknown champion/role values remain explicit; the engine never guesses a role from champion identity.

### Manual pregame planning

The **Plan prepartida** view is a separate mode and is always labeled **“Escenario manual. No representa datos de una partida activa.”** It never constructs a `GameState` or tracker snapshot and never updates live recommendation revisions, target stabilization, lifecycle/history rows, decision events, SSE notifications, alerts, or audio. It reads an immutable catalog copy and uses separate rule/profile managers so manual loading cannot reset the live coordinator.

The request requires a catalog-resolved champion, canonical role (`top`, `jungle`, `middle`, `bottom`, `support`), stage (`lane_start`, `first_recall`, `mid_game`, `late_game`), and explicit strategy. Gold is bounded to `0..100000`; inventory is at most seven distinct catalog-valid map-11 stacks with counts `1..99`. Each pressure is one of `unset`, `none`, `low`, `medium`, or `high` for physical, magic, crowd control, healing, shielding, armor, magic resistance, burst, sustained damage, slows, and lane pressure. Unknown JSON fields, trailing JSON, oversized bodies, invalid enums, and unresolved catalog identities are rejected.

An explicit pressure always wins. When a catalog-resolved lane opponent is supplied, only still-`unset` fields may be filled from the patch profile and catalog-only damage prior, and the response includes an assumption warning. Those conservative fills may score a neutral choice already activated by the user, but cannot activate neutral advice on their own. The planner never invents an enemy team, KDA, gold lead, win probability, or current-match fact. Confidence is capped at `0.80` in addition to profile-provenance, explicit-context, and trait-confidence caps. A valid conservative fallback may return HTTP 200 with `status: insufficient_context` rather than forcing a primary item without two distinct alternatives.

Rune, summoner, and initial-plan copy is stored in the strict patch-versioned generic profile registry. Each section has one to three stable `profile_curated` plain-text options with `when` and tradeoff fields. HTML/control characters and win-rate, pick-rate, tier, or “best” claims are rejected during registry compilation; lower-confidence fallbacks identify themselves as generic or assumed.

### Match lifecycle and event semantics

`/api/game` and `/api/dashboard` add the current connection state, local match session, decision revision, and a bounded recent-event view. SQLite history is authoritative; dashboard SSE remains a lossy latest-snapshot transport and is not the only copy of a semantic event. Event IDs and per-match sequences are stable, and `(match, dedupe_key)` uniqueness prevents cumulative Riot events from replaying after reconnect or restart.

Process restart recovery reloads the bounded event tail for an active outage and a recently ended session. SQLite also checkpoints the current outage class, outage start, latest failure time, and consecutive game-unavailable interval transactionally with the session, so mixed failure classes cannot bridge end grace across a restart. Legacy v2 rows restart unavailable grace because their later class transitions cannot be proven. This preserves exactly one connection-loss/restore pair and prevents the same still-visible terminal payload from creating another local match. Repeated identical inventory occurrences use the durable session sequence as an occurrence discriminator, while SQLite accepts a conflicting event key only when every stored event field matches exactly. If lifecycle persistence fails, the tracker retains the complete pending game snapshot with its session/events and publishes that coherent candidate only after the batch commits.

`match_started` is observed when cumulative `GameStart` evidence exists and otherwise uses an explicit inferred start estimate. Live Client terminal events, death-counter changes, and inventory multiset changes are observations. `item_purchased` and `returned_to_base` are explicitly marked `source: inferred`: the Live Client API does not expose authoritative shop transactions, recall completion, or player coordinates. Slot-only inventory reorder is ignored; component combines, sales/undo, and consumable removal are retained as conservative inventory classifications with confidence values and inferred classification provenance.

One `connection_lost` is recorded per outage and one `connection_restored` on same-match recovery. Transport timeouts, connection refusal, and invalid payloads never end a match by themselves. A terminal source event ends immediately; repeated explicit game-unavailable responses may end it only after `LOL_ENGINE_END_AFTER`. If a materially different fingerprint or large game-time rollback proves that a next match has begun first, the still-incomplete prior session is closed once with lower inferred confidence. Time/passive-gold/CS-only ticks still refresh raw telemetry but do not recalculate or persist a recommendation unless affordability crosses the current component cost or the bounded periodic refresh is due.

### Decision alerts

Decision events are a separate durable stream for changes that may matter to the player. They have stable IDs, an ascending per-session sequence, importance, game/clock time, neutral previous/current summaries, reason codes, and an optional link to the lifecycle event that caused them. SQLite schema v5 retains the v4 atomic stream and adds structured, versioned recommendation evidence to events/checkpoints: status, patch, phase, strategy, ranked choices, target/component, confidence, profile/rule provenance, and stable reason codes. Volatile timestamps, revisions, and warnings are excluded, so time-only polls do not write evidence. Exact retries are idempotent, while an identity collision with different content is rejected. New events must be sorted and contiguous from the stored checkpoint, and lifecycle cursors/links are checked against the same session's committed lifecycle history inside that transaction. A restart loads the checkpoint and latest event without replaying historical alerts as new.

The engine records only these transitions: a stabilized target change; a next-component change associated with a new inventory, inferred purchase, or inferred base event; the current component becoming affordable; a critical counter activating; inventory becoming full; data becoming stale; external rules becoming unavailable; connection restoration; and an actionable option update linked to an inferred `returned_to_base` event. Raw score changes, alternative reordering, passive-gold countdowns, timestamp/revision-only changes, and the initial page load do not create alerts. Because Live Client does not expose recall completion, the return-to-base alert is explicitly an inference from the existing inventory detector rather than authoritative location telemetry.

`GET /api/matches/{id}/decision-events` is the authoritative bounded history. `decision_notification` SSE messages are emitted only after the durable commit and are intentionally lossy live hints; reconnecting clients refresh REST history. A failed decision write is retained as an exact pending batch even if the match ends or a new session starts. Once recovered, an old-session batch remains in its authoritative history but is not replayed as a live notification or exposed as the new session's latest event. Copy describes changed or available options and never commands a purchase.

### Local match summaries and privacy

Lifecycle sessions, semantic events, the last observed self-player checkpoint, and structured recommendation evidence are the authoritative local sources. A summary is a rebuildable projection, not an additional telemetry authority. The checkpoint stores only champion, canonical role, level, KDA, CS, game time, observation time, and slot-insensitive inventory counts; it excludes Riot IDs, opponent identifiers, raw payloads, and match fingerprints. Its write is part of the lifecycle transaction. Late recommendation commits update their source revision and make an ended summary stale for bounded startup/periodic rebuilding.

A summary is labeled `complete_terminal`, `incomplete_inferred`, or `active`. Observed terminal evidence is kept separate from a match closed at a proved next-match boundary or conservative unavailable grace. Outcome is always `unknown` with `unavailable_locally`: the authoritative local-summary path never queries optional Match-V5 data or infers a win/loss. Duration uses the absolute game clock as a lower bound when attachment was late, rather than subtracting wall-clock observation times. Terminal KDA/CS means the last value actually observed. Inventory changes remain observed events; purchase and return-to-base rows keep their original `inferred` source, confidence, and caveat instead of being promoted to facts. Final choices and stable decision changes preserve the rule/profile provenance used at the time.

The builder allowlist-decodes source payloads, validates per-session sequence maxima, sorts/deduplicates deterministically, and records typed caveats instead of fabricating missing values. Malformed individual events degrade only their summary. Materialized JSON has a canonical SHA-256 semantic digest that excludes `built_at`; missing, stale, or corrupt rows are rebuilt with an optimistic source revision so an older build cannot overwrite newer evidence. Legacy v4 matches have no self-player checkpoint until a later live observation, and their summaries explicitly retain the corresponding limited-history caveat.

Retention is disabled by default. Users may opt into an age or maximum-completed-match policy; active sessions are never retention candidates, and deletion waits for pending recommendation persistence to settle. Exports are versioned allowlist JSON and never include Riot IDs, opponent identifiers, fingerprints, or raw source blobs. SQLite secure deletion is enabled and bulk erasure requests a WAL truncate checkpoint, but this is local best-effort maintenance—not a promise to erase filesystem snapshots, backups, or storage-device remnants cryptographically.

## Web dashboard

The main screen is optimized for a 1080p second monitor and remains usable at narrow browser widths. Tracker state, catalog provenance, and patch remain visible as the status row wraps into a compact grid; responsive layouts never hide those operational facts. The main decision card is designed to answer three questions at a glance: **which options are worth comparing**, **whether the suggested component is affordable**, and **why the primary option fits the current match**. It also shows:

- tracker, event-stream, and catalog status;
- current gold, player state, patch, map, game time, and last update;
- the primary target plus all two or three ranked alternatives, and analysis confidence;
- physical, magic, and true-damage estimates;
- the highest enemy threat with level, KDA, CS, and inventory value;
- the player's current inventory;
- a manual **Recalcular** action with pending, success, and error feedback;
- a bounded, session-scoped text history plus dismissible live toasts for important decision changes.

The ranked choices use neutral, choice-oriented language and an accessible ordered list. The dashboard prefers `ranked_options`, can reconstruct the list from `target_item` plus `alternatives`, and retains a compatibility fallback to the legacy singular `alternative`. The six-slot inventory count comes from the recommendation engine; the Live Client trinket slot is excluded from those capacity cells. Every engine warning is rendered in an accessible list instead of collapsing multiple cautions into one line.

The manual pregame view uses a keyboard-operable form with explicit labels, fieldsets, a focusable validation summary, catalog-backed selectors, and a polite result live region. Manual provenance, confidence, assumptions, all ranked choices, and the three curated plan sections are rendered with safe DOM text APIs. Live status badges and decision-alert controls remain outside the manual view, and manual responses never enter the toast, tone, or voice path.

The separate **Historial y privacidad** view uses identity-free cards for champion, role, duration, KDA, CS, completion state, and the explicit message **“Resultado no disponible.”** Detail badges spell out observed versus inferred inventory/purchase/base evidence and show confidence, caveats, final choices, and rule provenance without relying on color alone. Loading, empty, degraded, keyset pagination, export, retention, and confirmed deletion states are handled independently from the live, pregame, toast, and audio paths.

The same view contains a separate optional account/statistics panel. It never asks for or stores an API key in browser state. Account linking requires an explicit consent checkbox, synchronization is manual unless server-side auto-sync was enabled, and account deletion preserves the independent local Live Client history. Historical groups label game count, observed wins, minimum-sample warnings, role/rank provenance, uniquely determined same-role matchup, and final item multiset; text explicitly says these are observations rather than causal recommendations.

The UI explicitly handles loading, waiting for a game, catalog synchronization/unavailability, unsupported champion or mode, ready, saving for a component, full inventory, completed route, stale data, and calculation errors. Stale or disconnected advice is hidden or visibly paused rather than presented as current. Connection generations and aborted dashboard reads prevent an older response from reactivating advice after a newer event-stream disconnect.

The browser first reads `GET /api/dashboard`, then consumes complete `dashboard_snapshot` events from `/api/events`. Every dashboard snapshot includes an opaque server epoch in addition to its revision. A lower revision is accepted only after the epoch changes; the prior session, recommendation, toasts, and history view are paused/reset before the new snapshot can become live. Retired epochs are rejected so a delayed pre-restart response cannot reactivate old advice. After an event-stream reconnection the browser remains paused until it accepts a fresh dashboard read. Catalog source (`Data Dragon`, disk cache, in-memory cache, or active remote data that could not be persisted) and patch fallback are displayed independently: cached data is not automatically described as a patch mismatch, and a mismatch is warned regardless of source. In-memory fallback is identified explicitly, while unpersisted remote data warns that it could be lost after restart. Item icons are requested from the official Data Dragon CDN only after validating the patch and numeric item ID; text and dynamic elements use safe DOM APIs rather than HTML interpolation. A failed icon falls back to a local text tile. Unhashed CSS and JavaScript use HTTP revalidation rather than an hour-long freshness window, so restarting an updated binary cannot leave the old dashboard code silently active.

Decision text is always available. Audible alerts start in `off` mode and muted; the user must explicitly select tone or voice/unmute before Web Audio or speech synthesis can be used. Mode, clamped volume (`0..1`), mute, and a clamped minimum interval (`5..300` seconds) are stored locally. Event IDs are deduplicated per browser tab in `sessionStorage`, and visible toasts/history are capped. Live hints for another session are discarded before rendering or dedupe, and outstanding toasts are cleared at a session boundary. The browser tracks the highest sequence loaded from REST and silently reconciles history whenever the dashboard reports a newer durable event, covering missed/lossy SSE hints without replaying them as toasts. Stale data, informational events, duplicates, and events inside the cooldown never produce sound. Missing Web Audio or speech support degrades to text only. The page never requests operating-system notification permission.

### Screenshots

Screenshots will be added after the final real-match verification.

<!-- Screenshot placeholder: live ready/save state on a 1080p second monitor. -->

## Patch and offline behavior

At startup the backend requests Data Dragon's versions list and downloads:

- `{patch}/data/es_MX/item.json`
- `{patch}/data/en_US/championFull.json`

The official versions list also contains historical aliases such as `lolpatch_7.20`. The client ignores legacy or malformed entries, preserves the server order of valid three-part numeric patches, and fails only when no valid patch remains; the first retained entry is therefore the newest usable Data Dragon patch.

When an active game's `major.minor` matches a listed Data Dragon version, that patch is preferred. Otherwise the newest version is used and `catalog.fallback` is exposed. Failed live-patch synchronization uses bounded exponential retry rather than treating the patch as handled.

Cache lookup is locale-specific and prefers an exact game version, then the newest cached `major.minor` match, then the latest complete catalog as an explicit fallback. This local selection also runs when `LOL_ENGINE_DDRAGON_SYNC=false`. Remote or cache catalogs are accepted only when item map keys match their IDs and every retained recipe edge resolves.

## How the heuristic works

Data Dragon does **not** provide an authoritative physical/magic/true damage split. The MVP therefore uses a transparent approximation:

1. Champion tags provide a fixed prior (`Mage`, `Fighter`, `Marksman`, and similar).
2. Spell and passive text/markup contributes explicit physical, magic, and true-damage evidence.
3. Current offensive item stats adjust that profile.
4. Each opponent is weighted by level, KDA contribution, CS, completed items, inventory value, and level advantage.
5. Unknown or ambiguous champions use a neutral fallback and lower confidence.

Garen's state uses this fixed score:

```text
1.5 * level difference
+ 0.35 * KDA-contribution difference
+ CS difference / 30
+ spendable-value difference / 1000
```

Scores `>= 2` are `ahead`, scores `<= -2` are `behind`, and the interval between them is `even`. This is deterministic guidance, not a claim about perfect game understanding.

### Garen v2 match context

The v2 scorer also derives a deterministic context containing the game phase, explicit configured strategy, lane opponent, main threat, and normalized `0..1000` needs for physical/magic pressure, healing, shields, crowd control, slows, enemy armor, burst, and sustained damage. Early game weighs the same-position lane opponent more heavily; later phases progressively weigh the whole enemy team.

Healing, shields, CC, burst, and sustained-pressure values come from structured, patch-versioned champion and item traits in `rules/garen/v2/<patch>.json`. Live Client display names are resolved through the active catalog to canonical Data Dragon champion IDs before trait lookup, including punctuation and spacing aliases. Localized item descriptions are not parsed as policy. When a champion lacks structured traits, the response lowers `trait_confidence` and includes a warning instead of pretending the estimate is complete. `split_push` versus `teamfight` intent is configuration, never an inference from unavailable map-position telemetry.

### Rule loading and stability

Rule JSON is limited to 1 MiB per file, rejects unknown fields and trailing JSON, and validates schema version, bounds, signals, strategies, duplicate rule/item IDs, every champion-trait reference, and every candidate or item-trait reference against the matching active catalog. A nonzero missing-trait penalty is mandatory. Patch components are canonical decimal integers, so aliases with leading zeroes such as `16.18.01` are rejected. Resolution first requires raw equality with the exact active catalog patch, then uses the newest non-future file in the same `major.minor`; file discovery order cannot let a semantic alias override the canonical exact file. It never selects an arbitrary cross-patch file. A failed reload retains a compatible last-known-good ruleset; if none exists, the engine stays operational with the compiled legacy rules and exposes `source: legacy_builtin`, `fallback: true`, the last loading error, and a warning.

The scorer uses integer scores and stable rule-order/item-ID tie breaking. The coordinator holds a target until the challenger exceeds the configured margin for the confirmation duration and the minimum hold has elapsed. Stale intervals clear pending challenger evidence and pause the minimum-hold clock while preserving the incumbent. A completed/invalid incumbent or a critical counter threshold switches immediately. Component choice, affordability, and inventory status continue updating while the target is held.

Generic profiles are loaded separately from `rules/generic/v1/<patch>.json`; the bundled Garen v2 document remains byte-pinned by checksum and its fixed-time golden outputs are also compared with generic routing enabled. Generic JSON has the same 1 MiB/file and 128-file directory bounds, rejects symlinked roots/files, unknown/trailing JSON, noncanonical patch aliases, selector overlap, duplicate effective families/items, invalid roles/tags/copy, unsafe neutral candidates, and catalog-invalid candidates or recipe components. Every candidate recipe closure must be acyclic, purchasable, map-11 eligible, named, and positively priced. Resolution accepts the raw-exact catalog patch or the newest non-future file in the same `major.minor`, then a compatible validated last-known-good document. Fixed confidence caps are `0.95` (champion+role), `0.80` (champion default), `0.65` (role archetype), `0.50` (champion tag), and `0.25` (neutral).

Role and tag archetypes require positive structural tags and may exclude incompatible overlapping classes. Broad tags alone never convert an assassin into a mage family, an AP fighter into an AD-fighter family, or an enchanter into a tank family; ambiguous combinations fall through to a compatible lower-confidence profile or the neutral path. The neutral profile contains no unconditional core choice. Its candidates must be activated by explicit defensive pressure, and the engine returns `insufficient_context` unless activation leaves a primary plus two distinct alternatives. Data Dragon tags only establish structural compatibility; they are never presented as meta authority.

Physical armor and magic resistance are separate normalized signals. Armor comes only from structured armor stats and drives physical-penetration candidates; magic resistance comes only from structured spell-block/magic-resistance stats and drives magic-penetration candidates such as Void Staff. Localized item descriptions are not parsed to infer either signal, and explanations name `armadura` or `resistencia mágica` explicitly so the two pressures cannot be conflated.

## Recipe decisions

Candidate target IDs are stable engine identifiers, but every name, price, recipe, availability flag, and image comes from the active Data Dragon catalog. The engine:

- rejects removed, non-purchasable, and non-map-11 items;
- skips completed targets;
- consumes owned components as a multiset through the recipe graph;
- allocates shared components across sibling recipes to maximize immediate combines;
- recommends the highest-value affordable missing component;
- otherwise returns `save_for_component` with exact `gold_needed`;
- returns `inventory_full` instead of recommending a purchase that needs a seventh slot;
- requires at least two distinct, valid alternatives before emitting a Garen v2 `ready` or `save_for_component` result;
- requires a primary plus at least two distinct, valid alternatives before a generic profile becomes actionable; the neutral fallback otherwise returns `insufficient_context`.

## Deterministic local personalization

Personalization is optional and local. `balanced` is neutral; `safe` applies at most `+12` (or a `-3` tradeoff) to structured durability, tenacity, and survival coverage; `aggressive` applies at most `+12` (or a `-3` tradeoff) to structured damage, penetration, and mobility coverage. Preferred items receive a fixed `+8` bonus. Avoided items are excluded before ranking, except that a currently critical counter remains visible with an explicit override code. Bounded bonuses and feedback penalties may reorder ordinary choices, but the visible primary plus alternatives reserves required critical and baseline counter coverage before truncation. If exclusions leave fewer than a primary plus two alternatives, the engine returns `insufficient_context` instead of ignoring the preference. Stored item choices are revalidated against every active catalog patch; an item removed from map 11 or the shop disables the stored adjustment with `catalog_mismatch` status until the preferences are changed or reset. Each live calculation is bound to one catalog plus rule/profile/personalization revision; a concurrent mutation discards and retries the old calculation before stabilization or persistence.

Feedback starts disabled. When enabled, only explicit **Útil / No útil** input tied to the latest ready, durably persisted decision evidence and one of its still-visible ranked choices is stored. Session, patch, phase, strategy, champion/role, rule/profile provenance, personalization evidence, and ranked choices must still match the current recommendation; a context-only change hides the controls until matching durable evidence exists. Adjustment begins only after three matching samples, uses two score points per useful-minus-not-useful vote, is capped at `±9`, and is scoped to champion, role, phase, strategy, patch, profile, and rule version. It can reorder but never exclude a choice or remove all critical coverage. It does not read match outcome, win rate, opponent identity, raw payloads, or any network/AI model. Every applied effect has a stable reason code in recommendation evidence, and the aggregate summary reports the active threshold and cap.

The browser stores an in-flight feedback tuple in session storage before sending it. A lost or ambiguous response reuses the same client ID, decision, item, and value; it never creates a replacement vote. SQLite schema v10 also permits at most one record per durable decision and item even if different client IDs arrive. Feedback deletion commits first, records durable privacy maintenance, and retries a best-effort WAL truncate after busy readers release or on restart. As with other local deletion, filesystem snapshots and external backups remain outside the application's control.

Historical summaries and the read-only historical baseline remain observational records. They are neither rewritten by personalization nor treated as training or outcome labels.

Reset returns style/items/feedback to defaults immediately and advances the local preference version, which resets temporal stabilization before the next stable choice. Disabling feedback immediately removes learned feedback effects while leaving any independently selected style/item preferences explicit. The export endpoint contains only preferences and per-item aggregate counts. Feedback deletion uses `DELETE_PERSONALIZATION_FEEDBACK`; local match-history and Riot-account deletion remain separate operations.

## Configuration

| Environment variable | Default | Meaning |
| --- | --- | --- |
| `LOL_ENGINE_LISTEN_ADDR` | `127.0.0.1:8080` | Loopback-only HTTP bind. |
| `LOL_ENGINE_LIVE_CLIENT_URL` | `https://127.0.0.1:2999` | Loopback-only Riot endpoint. |
| `LOL_ENGINE_POLL_INTERVAL` | `2s` | Positive polling duration. |
| `LOL_ENGINE_STALE_AFTER` | `10s` | Time before previous game data becomes stale. |
| `LOL_ENGINE_END_AFTER` | `90s` | Conservative explicit game-unavailable grace before an inferred match end; must exceed the stale threshold. |
| `LOL_ENGINE_DB_PATH` | `data/lol_engine.db` | Local SQLite file. |
| `LOL_ENGINE_DDRAGON_BASE_URL` | `https://ddragon.leagueoflegends.com` | Absolute HTTP(S) source. |
| `LOL_ENGINE_DDRAGON_LOCALE` | `es_MX` | Item locale. |
| `LOL_ENGINE_DDRAGON_TIMEOUT` | `5s` | Per-request client timeout. |
| `LOL_ENGINE_DDRAGON_SYNC` | `true` | Remote startup/live-patch synchronization. |
| `LOL_ENGINE_RULES_DIR` | `rules/garen/v2` | Strict patch-versioned Garen rules. Missing or invalid rules use an explicit warned legacy fallback. |
| `LOL_ENGINE_GENERIC_ENABLED` | `true` | Enables tested non-Garen profile routing. Set `false` to retain the prior unsupported behavior. |
| `LOL_ENGINE_GENERIC_RULES_DIR` | `rules/generic/v1` | Strict patch-versioned generic profiles. Invalid or incompatible files fail closed unless a compatible last-known-good document exists. |
| `LOL_ENGINE_STRATEGY` | `balanced` | Item-scoring intent: `balanced`, `split_push`, or `teamfight`. |
| `LOL_ENGINE_RIOT_MODE` | `disabled` | Optional enrichment mode: `disabled`, private/developer `direct`, or registered HTTPS `backend`. |
| `LOL_ENGINE_RIOT_REGION` | empty | Required regional route when enabled: `americas`, `asia`, `europe`, or `sea`. |
| `LOL_ENGINE_RIOT_API_KEY` | empty | Private direct-mode BYOK read from the process environment only; never stored, returned, or sent to the browser. |
| `LOL_ENGINE_RIOT_BACKEND_URL` | empty | Registered HTTPS backend URL for backend mode. Plain HTTP is accepted only for explicit loopback development. |
| `LOL_ENGINE_RIOT_TIMEOUT` | `10s` | Optional Riot request timeout. |
| `LOL_ENGINE_RIOT_CONCURRENCY` | `4` | Service-wide Match-V5 detail limit across every account/filter (`1..8`). |
| `LOL_ENGINE_RIOT_PAGE_SIZE` | `20` | Bounded Match-V5 list page (`1..100`). |
| `LOL_ENGINE_RIOT_MAX_MATCHES_PER_SYNC` | `100` | Maximum normalized matches/pending fetches handled in one run (`1..1000`). |
| `LOL_ENGINE_RIOT_AUTO_SYNC` | `false` | Server-side opt-in startup sync for linked accounts. |
| `LOL_ENGINE_RIOT_RAW_STORAGE` | `false` | Must remain false; raw Match-V5 response storage is prohibited. |

## Optional Riot account and Match-V5 enrichment

This feature is an add-on, not a dependency of the live engine. `LOL_ENGINE_RIOT_MODE=disabled` performs no Account-V1 or Match-V5 request and needs no credential. Local tracking, recommendation, lifecycle history, summaries, export, and privacy controls remain available.

### Distribution boundary

- `direct` is for private developer use with the user's own key supplied through the process environment or an operating-system secret launcher. The key is held only in memory, sent only as `X-Riot-Token`, redacted by formatting/JSON/error types, and never written to SQLite, browser storage, HTTP responses, or logs. Redirects are rejected, so the token cannot be forwarded to another host or scheme.
- A publicly distributed product must use a registered HTTPS backend and the appropriate Riot production approval/RSO flow; it must not embed a shared personal/development key in the binary or frontend. The included `backend` adapter is deliberately narrow and does not claim that production approval already exists.
- As checked on **2026-09-19**, Riot documents Account-V1/Match-V5 regional routing in the [League of Legends developer documentation](https://developer.riotgames.com/docs/lol), API contracts in the [Developer Portal API reference](https://developer.riotgames.com/apis/), key/distribution rules in [General Policies](https://support-developer.riotgames.com/hc/en-us/articles/22698591841939-General-Policies), and production/RSO constraints in the [League of Legends policy](https://support-developer.riotgames.com/hc/en-us/articles/22698698001939-League-of-Legends). Recheck those policies before distribution because Riot may change them.

### Consent, synchronization, and privacy

Linking calls Account-V1 only after explicit consent. SQLite stores the linked user's PUUID because Match-V5 requires it, but never stores an opponent Riot ID/PUUID, raw response, browser-entered credential, embedded shared key, or raw successful Match-V5 ID. Schema v7 replaces durable successful/session-link IDs with installation-and-account-keyed opaque digests; schema v8 adds durable reconciliation state; schema v9 adds typed singleton personalization plus explicit feedback; schema v10 adds stable decision-choice feedback uniqueness. Delayed 404 retry IDs are authenticated-encrypted instead of stored as plaintext. The separate `<database>.riot-key` sidecar is `0600`; newly created storage directories are `0700`, and the database/WAL/SHM files are tightened to `0600`. Back up or move the database and sidecar together: an absent or replaced sidecar fails closed at startup instead of creating inconsistent keys. Normalized official storage contains match timing/patch/queue/map, the linked player's final facts/items, and at most the champion name of a uniquely determined same-role opponent. The v1→v10 migration is ordered and transactional; the privacy-sensitive v7 step compacts the migrated database and truncates the WAL before normal use resumes.

Each sync uses the linked account's consented regional route, freezes `endTime` at the run start, persists its route/window/cursor, preserves source page cardinality, bounds decoded IDs/pages/matches, shares one detail-fetch semaphore across all accounts, and resumes partial or cancelled work after restart. `Retry-After` seconds and configured fallbacks are positive and bounded before conversion, so malformed or overflowing values cannot bypass or push the shared credential/region gate into the past. A listed match that temporarily returns 404 becomes an encrypted durable delayed fetch. Repeated normalized facts are idempotent.

Ingestion and local-session reconciliation are separate durable outcomes. A completed ingestion transaction records reconciliation as `pending`; successful linking moves it to `complete`. A storage failure returns HTTP `202` with `status: complete`, `reconciliation_status: pending`, and a safe error kind—it is not reported as resumable Match-V5 ingestion. The next sync trigger retries reconciliation first and returns without opening another Match-V5 window; this remains true after restart. Reconciliation prefers an exact official ID; otherwise it requires every one of interval, champion, queue, map, and platform. Missing evidence stays pending, ambiguity stays ambiguous, and several local reconnect segments may link to one official match without counting it more than once. `OC1` uses `sea`; `ME1` uses `europe`.

Statistics group by exact `major.minor` patch, canonical role and its provenance, explicit rank bucket/provenance (`unknown`/`unavailable` today), unique same-role opponent champion when available, and a sorted final-item multiset that excludes trinket slot 6. They never infer historical rank, purchase order, causality, or mutate itemization rules. Local summary outcome remains `unknown`; observed official wins appear only in the clearly separate optional aggregate panel.

API endpoints are additive and return `Cache-Control: no-store`: `GET /api/riot/status`, `POST /api/riot/accounts`, `POST /api/riot/sync`, `DELETE /api/riot/accounts/{local-account-id}`, and `GET /api/history/statistics`. Deletion requires `DELETE_RIOT_ACCOUNT_DATA`, establishes a privacy fence before account resolution or sync writes, cancels and waits older writers, removes account-scoped official facts/checkpoints/pending links and orphan official rows, clears the service cache, and leaves local match history intact. SQLite uses `secure_delete` and attempts a WAL truncate, but the application cannot promise cryptographic deletion from filesystem snapshots or backups.

## Security and resilience

- The public server refuses non-loopback binds.
- API requests reject non-loopback `Host` values and any browser `Origin` that is not the exact same local authority, mitigating DNS rebinding and cross-origin POSTs.
- The embedded UI uses a restrictive Content Security Policy and loads no remote scripts, styles, fonts, or frameworks; only validated Data Dragon item images are allowed remotely.
- Browser alert preferences stay in local storage, decision dedupe stays in session storage, and no browser notification permission is requested.
- Riot's self-signed certificate exception applies only after loopback URL validation.
- The Live Client transport bypasses proxies, validates every resolved address, and dials numeric loopback addresses only.
- Live Client and Data Dragon responses have size, content, trailing-data, and schema validation.
- SSE uses bounded subscriber buffers and one-second write/flush deadlines, and exits on application shutdown.
- There is no memory reading, injection, clicking, purchasing, movement, or game automation.

## Verification

```bash
PATH=/opt/homebrew/opt/go@1.26/bin:$PATH GOTOOLCHAIN=local go fmt ./...
node --check internal/webui/assets/app.js
node --check internal/webui/assets/state.js
node --check internal/webui/assets/history.js
node --check internal/webui/assets/history-state.js
node --test internal/webui/state_test.js internal/webui/history_state_test.js
PATH=/opt/homebrew/opt/go@1.26/bin:$PATH GOTOOLCHAIN=local go test ./...
PATH=/opt/homebrew/opt/go@1.26/bin:$PATH GOTOOLCHAIN=local go test -race ./...
PATH=/opt/homebrew/opt/go@1.26/bin:$PATH GOTOOLCHAIN=local go vet ./...
PATH=/opt/homebrew/opt/go@1.26/bin:$PATH GOTOOLCHAIN=local go build ./...
```

Tests use temporary SQLite files and in-memory HTTP transports; they never contact Riot or Data Dragon. Coverage includes ordered v1→v10 migration, personalization reopen/logical idempotency/default compatibility, feedback WAL deletion recovery, raw-ID removal from DB/WAL/SHM, restrictive modes under a permissive umask, reopen/rebuild/resume, reconciliation-pending restart recovery, redirect attacks, timeout and overflowing `Retry-After` classification, bounded ID decoding, shared-rate-gate isolation/eviction, link/delete fences, cross-account detail concurrency, stored-region routing, full sync-to-reconciliation, duplicate retries, corrupt sources, retention/deletion fences, safe export, deterministic digesting, redaction, and executable browser-state tests.

An excluded live smoke test is available only with the `riotlive` build tag plus `LOL_ENGINE_RIOT_LIVE_TEST=1`, `LOL_ENGINE_RIOT_API_KEY`, `LOL_ENGINE_RIOT_REGION`, `LOL_ENGINE_RIOT_TEST_GAME_NAME`, and `LOL_ENGINE_RIOT_TEST_TAG_LINE`. It resolves the opted-in account, lists one ID, and normalizes one detail without printing raw data:

```bash
PATH=/opt/homebrew/opt/go@1.26/bin:$PATH GOTOOLCHAIN=local \
  go test -tags=riotlive ./internal/riotapi -run TestLiveSmoke
```

Embedded asset tests verify the root page, stylesheet, JavaScript, favicon, MIME types, API namespace isolation, authority checks, and safe browser-source contract without starting a server or making network requests.

## Current limitations

- Live recommendations remain limited to Summoner's Rift. Five champion/role pairs have explicit curated profiles; other champions deliberately use lower-confidence champion, role, tag, or neutral fallbacks.
- Damage typing and ahead/even/behind state are documented heuristics, not Riot-provided truth.
- Structured matchup traits are intentionally incomplete; unknown profiles lower confidence and require measured expansion.
- No live rune detection, automatic rune/summoner selection, champion-select integration, causal historical coaching, overlay, Codex, or paid AI. Optional Match-V5 aggregates require consent and private/approved server configuration; local history still reports outcome as unavailable rather than guessing it. Pregame rune/summoner/initial options are curated profile comparisons, not detected loadouts or meta claims. Browser voice is an optional neutral live-decision cue, not generated coaching or a purchase command.
- Exact return-to-base and purchase actions remain inferred because Live Client exposes neither coordinates nor authoritative shop transactions.
- A transport outage alone never proves match completion. Without a terminal Live Client event, only repeated explicit game-unavailable responses beyond the conservative grace can infer an end.
- Real Live Client/browser verification is postponed until League of Legends can be restarted on the development machine. Unit and integration tests use deterministic local fixtures meanwhile.

## Next step

Restart League of Legends, enter the Practice Tool, and perform the final real Live Client plus browser walkthrough. After that, expand item rules using measured match data without coupling the pure engine to HTTP, SQLite, or the embedded UI.
