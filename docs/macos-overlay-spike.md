# macOS overlay spike: conditional feasibility, not game proof

**The native candidate compiles and its deterministic checks pass; coexistence with League is still unproven.** Continue only to bounded human testing, not a permanent shell decision. T2 provides an experimental AppKit panel; independent native transport checks passed, while T3 real-game observations remain pending. No GUI or game was launched during implementation.

## Build, test, then manually run

From the repository root on macOS arm64 with Xcode installed:

```bash
bash spikes/macos-overlay/check.sh help
bash spikes/macos-overlay/check.sh test
bash spikes/macos-overlay/check.sh build
```

The script fails on errors and never launches a GUI. It uses the explicit Xcode `swiftc`, `MacOSX.sdk`, target `arm64-apple-macos14.0`, and ignored `spikes/macos-overlay/.build/` products/module cache. No tool installation or system selection change is needed. Other architectures are not established.

For **human testing only**, start the existing server separately using the [README quick start](../README.md#quick-start), then run the binary in a separate terminal:

```bash
spikes/macos-overlay/.build/overlay
```

The **RW status menu** independently provides show/hide, next screen, top-left/top-right placement, interaction toggle and Quit. Click-through is on initially. Enable interaction to try dragging the panel background or its Hide button; this is not keyboard/text interaction. Use RW to regain control, hide or quit; if the menu is inaccessible in fullscreen, leave that Space first. Terminal Control-C is an additional escape. No hooks, global shortcuts, game automation, Accessibility/Screen Recording requests, webview, installer, signing or `.app` packaging are included.

## What the candidate does

| Boundary | Deliberately bounded behavior |
| --- | --- |
| Window hypothesis | Nonactivating, non-key/non-main `NSPanel`, floating level, `canJoinAllSpaces` and `fullScreenAuxiliary`; not proof of above-game placement or preserved focus. |
| Transport | Fixed GET `http://127.0.0.1:8080/api/dashboard`, matching `Origin: http://127.0.0.1:8080`; sequential requests, next poll two seconds after completion. |
| HTTP limits | 3-second request / 5-second resource timeout, 1 MiB decoded-body cap while streaming, HTTP 200 JSON only. All redirects (including same loopback), authentication challenges, other origins/statuses and oversized bodies fail closed. |
| Local isolation | Ephemeral session, no proxy, cookie store, credential store or URL cache; no configuration flags, persistence or raw payload/error logging. Only needed dashboard fields are decoded, not player identity. |
| Freshness | UI reevaluates independently every 250 ms, including while a request hangs. `last_success_at` must not be future-dated and must be less than ten seconds old. HTTP success, dashboard `updated_at` and cached snapshots alone never mean live telemetry. |
| Advice | Only fresh `in_game` / `connected` telemetry with an active session, supported map/mode, ready matching catalog patch, exact source-game revision, and `ready` or `save_for_component` plus valid target/component. Other states clear the previous advice. Warnings are bounded to three 200-character lines; the small panel can clip long copy. |

**Conservative provenance limitation:** `source_game_revision` must equal `game.revision`; passive time/gold ticks legitimately advance the game without recalculating advice. This spike pauses in that case, potentially until the coordinator's periodic recalculation. It does not invent a session binding for older recommendations. `catalog.source: cache` is not a telemetry freshness signal, nor is catalog patch proof of installed game version. Successful telemetry polls can update the tracker without publishing a new dashboard; retained dashboards can therefore conservatively expire despite a healthy tracker. Neither behavior changes the server.

## Compiler/test evidence versus observation

Host evidence supplied for this task: macOS **27.0.1**, build **26A434**, arm64; Swift **6.4.2**, installed Xcode SDK metadata **27.2**. The earlier `xcrun --show-sdk-version` lookup failed; it was not retried or repaired. Explicit SDK compilation and synthetic policy/HTTP delegate tests succeeded. Tests cover clearing advice, local expiry without completion, Go fractional timestamps/integer fields, ready/save behavior, incoherence and redirect denial; the original checks do not connect to a server or establish AppKit focus, Spaces or League behavior. A separate verifier passed 26 additional synthetic policy checks and 16 real URLSession exchanges against an exclusively owned synthetic loopback server: origin/path/header checks, redirect/auth denial, proxy/cookie isolation, exact/oversized streaming bodies, and stalled/truncated responses. Its redirect/proxy trap received zero connections. These are native wire observations, not game observations.

Observed verification: Swift tests reported 31 passes / `0 failure(s)` and the overlay build succeeded without launching. These regression commands also passed (two Go packages, 23 Node tests, clean tracked diff):

```bash
go test ./internal/api ./internal/webui
node --test internal/webui/state_test.js internal/webui/reconnect_test.js
git diff --check
```

The native consumer sends the existing exact local Origin and leaves Go loopback/Host/Origin/CSP protections unchanged. API/webui Go and browser-state Node checks are separate regression checks, not native/browser equivalence. **Native requests do not exercise browser CSP; browser compatibility is not established by this spike.** A different process on the fixed loopback port remains a trust limitation. Risk item 2 (new native transport boundary) received independent verification with no candidate-caused blocker. The verifier's API execution was unavailable because its isolated offline cache lacked dependencies; the writer's targeted API tests and parent's full `go test ./...` passed. Parent spot checks also passed Swift tests/build and all 35 Node tests.

## Human observation matrix — all pending

Record for each run: macOS version/build: ___; machine/architecture: ___; League version: ___; display resolution/scaling: ___; monitor arrangement: ___; Spaces/Stage Manager settings: ___; game graphics/display settings: ___. Do not record player identifiers or raw payloads.

| Configuration | Show/hide and above-game visibility | Input focus, default click-through, interaction toggle | Regain RW controls / hide / quit | Screen/corner placement and Spaces |
| --- | --- | --- | --- | --- |
| Ordinary desktop | Untested | Untested | Untested | Untested |
| LoL windowed | Untested | Untested | Untested | Untested |
| LoL borderless | Untested | Untested | Untested | Untested |
| LoL native fullscreen | Untested | Untested | Untested | Untested |

For each row, record pass/fail/not available and precise settings. Verify no game focus loss when showing; mouse reaches the game with click-through on; interaction can be regained and disabled; show/hide and quit remain practical after switching Spaces/displays. Separately observe advice clearing on backend stop and telemetry loss, then recovery only with fresh coherent data. A failed fullscreen row is a documented unsupported configuration, not permission to use injection or weaken security.

**Decision gate:** go only to this observation matrix. No-go on permanent framework selection or distribution until real supported modes and input behavior are recorded; independent native boundary checks have passed. S2/S4/S5 coexistence/visibility/focus proof, S3 tested game configuration, and S8 demonstrated-option recommendation are not complete; T3 stays pending. T2 implements the hypotheses and S6 bounded consumption, preserves S7/S9 boundaries, and prioritizes S10 macOS. No Riot approval is assumed; check current official policies before any later distribution.

## References — unverified, not evidence

Earlier Apple retrieval required JavaScript and yielded no usable API text. These URLs are navigation aids only, not fetched proof of behavior or Riot approval:

- [NSPanel](https://developer.apple.com/documentation/appkit/nspanel)
- [nonactivatingPanel](https://developer.apple.com/documentation/appkit/nswindow/stylemask-swift.struct/nonactivatingpanel)
- [fullScreenAuxiliary](https://developer.apple.com/documentation/appkit/nswindow/collectionbehavior-swift.struct/fullscreenauxiliary)
- [ignoresMouseEvents](https://developer.apple.com/documentation/appkit/nswindow/ignoresmouseevents)
