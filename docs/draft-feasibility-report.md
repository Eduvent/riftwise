# Live Draft feasibility: conditional go

**Outcome for [issue #8](https://github.com/Eduvent/riftwise/issues/8): conditional go for bounded macOS-first follow-up work, not approval to ship the full Draft engine.** Evidence gathered on November 11–13, 2026 supports limited live observation and identifies free catalog/professional-draft data candidates. Production source rights, representative SoloQ/rune evidence, eligibility and lifecycle behavior remain conditions, not completed proof.

The [approved contract](live-draft-contract.md) and [synthetic fixtures](draft-fixtures.json) remain unchanged. Every displayed pick must have a finite internal rating within **0.0..5.0 inclusive**, higher-is-stronger, in descending order; it is not a win probability. Missing evidence is not score 0.0. No formula, weights, source selection, gameplay efficacy or Riot approval is established here.

## Decision and reading path

- Use this report for findings, source comparison, policy boundaries and scoring alternatives.
- Use the [validation and follow-up plan](draft-validation-plan.md) for bounded work proposals, automated checks and separately authorized client trials.
- Only free providers are considered. Free access does not imply permitted redistribution, free infrastructure or permission to collect player data.
- Prioritize a simple human visual check of observed picks and suggestions before the randomized internal laboratory. Reuse overlay #5 and runes #7; exact pick order is deferred, low priority.
- This closes exploratory research with explicit unknowns; it does not silently narrow the approved product to a manual form or curated-only engine.

## Evidence ledger

**Observed** means measured in this investigation; **documented** means provider text, not independently audited service/data; **inferred** is a proposal; **unknown** is not a negative capability claim.

| ID / evidence date | Kind | Finding | Boundary |
| --- | --- | --- | --- |
| E1 / Nov 13 | Observed | CA-verified, authenticated loopback GETs returned a champion-select phase/session and teams of five; local assignment was `middle`. | One installed macOS client; client version and actual queue identifier were not collected. No TLS bypass, client write or policy approval. |
| E2 / Nov 13 | Observed | One bounded timeline captured completed-pick counts **4→5→6→7→8→9→10**, then `ChampSelect→InProgress`; 78.699 seconds, 40 cycles, eight change records, zero suppressed, process exit 0. | User described custom Tournament Draft with bots. Opening/first four picks and ban transitions were missed. Globally grouped champion IDs did not establish team composition. |
| E3 / Nov 13 | Observed + human corroboration | The user confirmed choosing middle before queueing, matching the prior observed `assignedPosition=middle`. | Consistency for that trial only; not proof of every queue/role, a swap or an informal agreement. |
| E4 / Nov 13 | Observed | A second timeline captured **5→6→7→9→10**, then `InProgress`; 66.509 seconds, 34 cycles, six change records, zero suppressed, exit 0. | Middle stayed stable; local pick order stayed `null` / `order_unknown`. No pick-order or role swap was validated. Skipping count eight illustrates sampling limits. |
| E5 / Nov 13 | Observed | Two other attempts immediately found `InProgress` and exited successfully as outside the Draft window. | No selection baseline or swap evidence; no invented explanation for disagreement with the reported search state. |
| E6 / Nov 11–13 | Documented | Riot describes unsupported LCU access, product/endpoint registration, static catalogs and access/policy boundaries [R1–R2]. | Public documentation is not a registered product, approval or guarantee of stable endpoints. Two additional Riot support pages returned 403; their content was not assumed. |
| E7 / Nov 13 | Documented | ChainCC and GPTilt describe free professional-game datasets [R6–R8]; Data Dragon supplies static definitions [R1]. | No CSV was downloaded or validated. Provider declarations do not independently settle upstream rights, completeness, rune coverage or SoloQ applicability. |
| E8 / Nov 13 | Inferred | Observed completed picks plus compatible catalog/context could support a small automatic observer/evaluator and human validation surface. | Requires explicit follow-up acceptance, supported eligibility observations and coherent lifecycle handling; it is not an implemented engine. |
| E9 / Nov 13 | Unknown | Ownership/queue eligibility, role blanks/updates/trades, recovery, stale/order/session races, installed version and queue coverage remain unverified. | Keep affected advice collecting/unavailable. Informal role swaps may be unobservable; exact pick order is intentionally deferred. |

### Observation method and limitations

Fresh user directions authorized each trial; no observer remains active. An ephemeral process read the specifically authorized lockfile, kept credentials and the official [public CA][riot-ca] in memory, and used certificate-chain and hostname verification. Only `GET /lol-gameflow/v1/gameflow-phase` and, during selection, `GET /lol-champ-select/v1/session` were allowed. No player/chat endpoints, alternate credential discovery, game actions or rune writes were used.

The method used a two-second cadence, three-second request deadlines, bounded responses and a ten-minute maximum; it stopped on errors or selection exit. Only allowlisted summaries and completed champion selections were emitted, not hovers, player/cell/session identifiers, credentials or raw responses. The official CA documentation discusses the Game Client; its successful verification here establishes only compatibility with this observed LCU client, not a general LCU support promise.

Phase and session reads were **not atomic**. Observer-local indexes/times are not native revisions, freshness guarantees or exact pick chronology. A repeated banned champion ID was preserved in one capture; another had ten completed ban actions but nine positive champion IDs. Causes were not established and identifiers must not be silently deduplicated into a fabricated interpretation. The conservative order helper's failure is not proof that LCU lacks order information.

One preflight isolation command unintentionally created a temporary directory outside the project. This was disclosed; contents were not inspected or cleaned, so emptiness was not asserted. No explicit program-file, CA, credential or raw-response write was part of the command. This incident is not evidence of a production-safe observer. Private diagnostic logs and filesystem paths are not published here.

## Source comparison: separate catalog, population and rights

Sources were reviewed on Nov 11–13, 2026; dates below are documentary observations, not ongoing freshness guarantees. **Unknown** access limits, licenses or coverage must be resolved before integration, not replaced with assumptions.

| Candidate | Coverage, patch and cadence | Free access, rights, rates and privacy | Assessment |
| --- | --- | --- | --- |
| **Riot API / Match-V5 and player APIs** [R1–R3] | Potential finished-match/personalization route, not a ready statistical population. Queue, region, rank provenance, patch window, samples and refresh depend on collection design. Detailed reference schema was not extractable in the final pass. | Keys/access tiers and rate limits apply; exact available quota not established. Personal keys cannot serve public consumption [R2]. Collection, retention, player consent/privacy and derived-data rights require separate design; backend operation is not necessarily free. | Conditional route for **SoloQ aggregates**, not a provider selected for implementation. History/mastery may inform familiarity, not prove skill or efficacy. |
| **Data Dragon** [R1] | Versioned champion/item/rune definitions and assets; no match denominator, queue/rank/region performance or counters. Manual updates may lag patches; regional/client versions can differ. | Public files without a provider subscription; applicable Riot use/asset terms still apply. Cache compatible versions; no private player data is needed. No exact rate limit established. | Catalog foundation candidate, not empirical pick or rune evidence. A cached catalog patch is not the installed client patch. |
| **Oracle's Elixir** [R4] | Professional-data lead; current season fields, samples, patch coverage and update cadence were not verified because download pages were not extractable. No SoloQ evidence established. | User-supplied free-CSV/provenance claims could not be confirmed from retrieved provider text. Access, attribution, redistribution, limits and player-data handling remain unresolved. | Retain as a pro-draft candidate; extraction failure is not evidence of poor dataset quality. |
| **Riot/GRID Open Access** [R5] | Esports, not SoloQ. Actual schema, historical coverage, Worlds 2026 scope and exact delay were not established. Riot's visible Nov 12 announcement said live Nov 14; GRID said launching Nov 12. | GRID advertises free early-stage research and explicitly says commercial use **No**. Documentation requires sign-in; no account/access was tested. Rates, retention and redistribution remain unknown. | Research lead, not a confirmed product dependency. As of the Nov 13 check, do not resolve the conflicting launch statements by assuming availability or a five-minute delay. |
| **Leaguepedia** [R9] | Supplementary professional tournaments/teams/results; not SoloQ. Direct copyright page returned 403, so current coverage/schema/cadence were not audited. | GPTilt declares Leaguepedia Cargo provenance and CC BY-SA 3.0; this is the dataset publisher's statement, not verification of every upstream right. API limits and privacy/attribution obligations require review. | Context/provenance lead, not automatically independent evidence from datasets derived from it. |
| **CommunityDragon** [R10] | Static client data/assets, not results. Inspected Ahri `roles` were classes (`mage`, `assassin`), not lane frequencies. A single record cannot establish all files' capabilities. Patch compatibility/cadence must be checked. | Public asset access does not independently establish redistribution rights or stable formats. Documentation returned 522 in the last pass; no quota guaranteed. No player collection needed for static use. | Optional catalog supplement. |
| **ChainCC Open Dataset** [R6] | Provider documents professional player/team rows, picks/bans/results/patch/league, seasons 2014–2026 and update date Nov 13. Rank/SoloQ coverage and rune fields not established; exact completeness/cadence unverified. | Declares no account/key/rate limit, CC BY 4.0 and commercial reuse with attribution. Upstream provenance/authority to license components needs review. Player names/data must be minimized before reuse. | Strong newly documented free **pro-draft research candidate**, not confirmed SoloQ evidence. |
| **GPTilt datasets** [R7–R8] | Esports card documents one game per row, patch/teams/winner/role-ordered picks and bans/tournament; weekly refresh claimed, initial June 2026, older-data and major-region/tier biases. No rune coverage established. | Dataset card declares CC BY-SA 3.0 and intended noncommercial research; repository LICENSE is CC BY-NC 4.0. Clarify scope and upstream rights. BY-SA itself does not prohibit commercial use. No quota/availability guarantee established. | Candidate for pro research/contrast; repository, dataset and upstream licenses are not interchangeable. |
| **PandaScore** [R11] | Free fixtures/context, not the detailed historical population needed for Draft. | Pricing page lists €0 context versus detailed historical/post-match data from €400/game/month. No paid tier or trial is recommended. | Defer under the free-only constraint. |
| **Meraki** [R12] | Static champion/item data; inspected Ahri has `positions: [MIDDLE]`, not a frequency denominator. `latest` does not prove a particular patch or update cadence. No rune recommendations established. | Public CDN; README requests credit/cache and describes Wiki-derived ability data. MIT project license does not independently settle all upstream data/asset rights. No availability/quota guarantee. | Supplementary role-possibility clue after provenance review, not measured likelihood or complete flex coverage. |
| **Previously reviewed scraped ranked dataset** [R13] | Historical candidate; freshness, queue/rank/region denominators and sampling not verified. | Dataset card declares CC BY 4.0; upstream access/redistribution rights not established. No data downloaded. | Not selected; a license label alone is insufficient. |

### Recommended interpretation, not source selection

The initial curated-first proposal was **premature for professional-draft research**: ChainCC/GPTilt describe ready datasets worth evaluating without first building a Match-V5 collection pipeline. Curation remains a possible explanatory/missing-data tool, not an approved replacement for the product contract.

For investigation, use Data Dragon as the catalog candidate and prioritize a bounded ChainCC documentation/schema/provenance audit; retain GPTilt and Oracle's Elixir for contrast where access/rights permit. ChainCC documents 99,150 player rows and 19,830 team rows for 2026, not that many independent matches. Its documented join is `game_id` **and** `side`; joining only on game ID doubles player rows. No such join or dataset-quality check ran here.

Keep professional, SoloQ and personal-history populations separate. Deduplicate the same games across providers; different vendors may share upstream material. Report match/participant denominators, collection window, patch, region, queue, rank provenance, exclusions and sparse groups. Pro frequency is not automatically SoloQ suitability; winning with a champion does not establish a causal counter or synergy. Rune alternatives still require their own source/validity evidence under #7.

Existing `internal/riotapi/normalize.go` retains linked-player match context, role provenance, outcome/items and a uniquely matched same-role opponent; it does **not** retain rune choices or create a representative population. `internal/statistics` is observational reuse material, not already a Draft evidence service. Reuse catalog identity/snapshot helpers named in the contract; do not repurpose item scores, manual pregame copy or fixture ratings as champion-strength data.

## Policy and access findings

Official Riot documentation [R1–R2], consulted Nov 11–13, says products serving players require registration even without official APIs. LCU is unsupported, without documentation/uptime/change guarantees, and developers are asked to disclose endpoint use in a registered application. Registration status and approval for Riftwise's proposed use were **not established**.

The same documentation contemplates highlighting important decisions and multiple choices, and restricts exposing hidden players or previously unknown session information. This is neither blanket approval for all dynamic recommendations nor a blanket ban on them. Unresolved application of policy to features/overlay must be clarified before distribution; inaccessible support pages cannot supply missing permission. User approval of a product contract or local trial is not Riot approval.

Future integration must use minimized authenticated loopback access with certificate/hostname verification, explicit supported endpoints and no chat/hidden-player discovery, game control, automatic champion/rune selection or credential exposure. Live esports delays are separate from the local champion-select observer. GRID's research-only access does not authorize using its outputs in a commercial product.

## Explainable scoring options: decision deferred

All options below must first reject unsupported context and ineligible champions, retain feasible enemy flex assignments and attach internal source/version/patch/session metadata. None selects weights, thresholds, tie policy, calibrated confidence or actual champion ratings. Display multiple supported alternatives with plain reasons, not one forced action.

| Approach | Explainable output | Evidence needed / tradeoff |
| --- | --- | --- |
| **Versioned curated rubric / ordinal bands** | Human-reviewed role/composition/interaction criteria map to the inclusive scale with readable reasons. | Named authors/reviewers, valid champion/role/patch coverage and independently reviewed examples. Easy to inspect; maintenance-heavy and not empirical strength/frequency. A curated-only first scope needs approval. |
| **Evidence-backed components** | Separate supported role suitability, matchup, team interaction and familiarity contributions, then a bounded internal score. | Licensed, applicable populations; denominators/sparse-group policy; overlap, confounding and patch drift review; approved normalization and weights. An observed win rate is not a causal counter or a calibrated score. |
| **Hybrid evidence plus explicit rules** | Empirical components where justified, reviewed rules for supported qualitative interactions, with honest limitations. | A documented combination policy and reasons tracing to each source; missing required components must suppress advice, not silently fabricate precision or become 0.0. More coverage, but more audit/maintenance work. |

Mains preference also needs a later decision: a tie-break limits distortion but rarely changes unequal rankings; a bounded bonus is more visible but may crowd out stronger non-mains and interact with the score ceiling; a lexicographic familiarity priority risks overpowering draft fit. In all cases, retain eligible non-mains, never create availability from preferences, and treat absent preferences as no personalization. No variant or amount is selected here.

Before formula selection, approve evidence populations, component meanings, source/patch applicability, minimum-evidence rules, mains tradeoffs and reproducible independently reviewed reference cases. Test arithmetic/ordering separately from strategic quality. A later quality study needs independent expert/reference judgments or appropriately designed held-out evidence, split by game/time/patch with leakage and confounding controls; matching a professional historical pick or generating random drafts alone does not prove efficacy.

## Closure boundary

This report plus the [validation plan](draft-validation-plan.md) fulfill the investigative deliverable by making conditions and gaps explicit. They do not prove full-client coverage, trustworthy current statistical/rune integration, approved source rights, a working evaluator, a UI, an overlay or a laboratory. No formula, implementation issue, provider integration, dataset collection or new client trial is authorized merely by publishing or closing #8.

### Dated references

R1–R2 and R12–R13 were consulted during Nov 11–13; the user-proposed candidate audit R3–R11 was Nov 13, 2026. Fetch failures/limited extraction are recorded above; links are leads, not assertions of accessible current content.

- **R1** [Riot LoL documentation: LCU, policies and Data Dragon][riot-lol].
- **R2** [Riot developer portal: access and application policy][riot-portal].
- **R3** [Riot API reference][riot-api]; detailed schema not extracted in the final pass.
- **R4** [Oracle's Elixir downloads][oracle]; page extraction unavailable.
- **R5** [Riot esports announcement][riot-grid] and [GRID Open Access][grid].
- **R6** [ChainCC provider dataset documentation][chaincc]; no CSV inspected.
- **R7** [GPTilt repository README][gptilt] and [repository license][gptilt-license].
- **R8** [GPTilt esports dataset card][gptilt-card].
- **R9** [Leaguepedia copyrights][leaguepedia]; direct fetch 403.
- **R10** [CommunityDragon asset docs][cdragon] and [inspected Ahri record][cdragon-ahri].
- **R11** [PandaScore pricing][pandascore].
- **R12** [Meraki README][meraki], [license][meraki-license] and [inspected Ahri record][meraki-ahri].
- **R13** [Historical ranked dataset card][ranked-card]; not endorsed.

[riot-lol]: https://developer.riotgames.com/docs/lol
[riot-portal]: https://developer.riotgames.com/docs/portal
[riot-api]: https://developer.riotgames.com/apis
[riot-ca]: https://static.developer.riotgames.com/docs/lol/riotgames.pem
[oracle]: https://master.d36liwrx5rvjnc.amplifyapp.com/tools/downloads
[riot-grid]: https://lolesports.com/en-US/news/build-with-official-league-of-legends-esports-data
[grid]: https://grid.gg/riot-open-access/
[chaincc]: https://chaincc.lol/free/data
[gptilt]: https://raw.githubusercontent.com/gptilt/datasets/main/README.md
[gptilt-license]: https://raw.githubusercontent.com/gptilt/datasets/main/LICENSE
[gptilt-card]: https://huggingface.co/datasets/gptilt/lol-esports-matches/raw/main/README.md
[leaguepedia]: https://lol.fandom.com/wiki/Leaguepedia%3ACopyrights
[cdragon]: https://communitydragon.org/docs/assets
[cdragon-ahri]: https://raw.communitydragon.org/latest/plugins/rcp-be-lol-game-data/global/default/v1/champions/103.json
[pandascore]: https://www.pandascore.co/pricing
[meraki]: https://raw.githubusercontent.com/meraki-analytics/lolstaticdata/master/README.md
[meraki-license]: https://raw.githubusercontent.com/meraki-analytics/lolstaticdata/master/LICENSE
[meraki-ahri]: https://cdn.merakianalytics.com/riot/lol/resources/latest/en-US/champions/Ahri.json
[ranked-card]: https://huggingface.co/datasets/HakimT/lol-champion-ranked-stats
