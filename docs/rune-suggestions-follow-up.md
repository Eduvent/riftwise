# Title: Suggest runes automatically from live champion, role and draft context

## Goal

Add a separate enhancement for automatically refreshed rune suggestions alongside live Draft assistance. Once champion and role context are observable, show relevant rune choices and concise reasons on the same second-screen/overlay presentation surface. No per-match request form and no automatic rune application are included.

**Separate proposed enhancement; runtime implementation is not approved by this issue.** Keep it linked from #4. Existing curated manual pregame rune options are not implemented live rune inference.

## Scope

- Consume the approved live context boundary: selected/hovered champion with its observation status, detected role or explicit unknown, observable draft context and compatible patch/versioned rune guidance. Distinguish a provisional hover from a locked selection.
- Refresh when champion, role, relevant draft context or supporting guidance changes. Explain meaningful choices/tradeoffs without promising win probability or efficacy that has not been validated.
- Use trusted curated/statistical guidance with internal source/version/patch metadata. Source selection and suitability criteria require implementation-time review; this proposal does not invent a ranking formula.
- When champion/role is missing, context is stale, or guidance is absent/incompatible, show waiting/collecting/unavailable copy as appropriate and clear obsolete actionable rune advice. Do not silently substitute another champion's or patch's page.

## Acceptance criteria

- [ ] Approve the rune-context and evidence requirements separately from #4's champion-pick product definition.
- [ ] Show multiple supported rune alternatives with concise reasons when available; identify provisional champion context without claiming it is locked.
- [ ] Automatically refresh on observable champion/role/context changes; prevent older work from overwriting current suggestions.
- [ ] Cover unknown champion/role, absent/incompatible guidance, stale/client loss, recovery and draft end/new session with deterministic acceptance cases before runtime work.
- [ ] Retain internal provenance and patch compatibility; avoid fabricated confidence, unsupported efficacy or match-outcome probabilities.
- [ ] Verify missing-data behavior clears prior suggestions without requiring a per-match user request.
- [ ] Keep rune application manual. Any client write or auto-apply feature requires separate explicit approval and policy review.

## Dependencies and exclusions

- [#4](https://github.com/Eduvent/riftwise/issues/4) defines the live Draft context and presentation-validity boundary. Its artifacts do not deliver an implemented observer or overlay.
- Coordinate with [#5](https://github.com/Eduvent/riftwise/issues/5) wherever its approved capabilities are required; confirm that dependency boundary before implementation. Referencing #5 does not authorize new persistence or expand either issue.
- See the [live contract](https://github.com/Eduvent/riftwise/blob/docs/4-manual-draft-contract/docs/live-draft-contract.md) and [replacement #4 body](https://github.com/Eduvent/riftwise/blob/docs/4-manual-draft-contract/docs/issue-4-live-draft.md). Branch-qualified links are provided until merge.

No persistent-player-data subsystem, LCU capture, overlay host, hidden-player identification or rune-page writing is hidden in this enhancement. Those integration prerequisites belong in separately approved work. Recheck Riot policy and endpoint usage before any future client integration; this draft claims neither policy approval nor real-client proof.
