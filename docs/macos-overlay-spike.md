# Native overlay over League of Legends on macOS

**The player confirmed that the panel is visible and passes clicks to the game in both Fullscreen and Borderless on the tested Mac.** The table below records the observed configuration and control checks.

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
| Window hypothesis | Nonactivating, non-key/non-main `NSPanel`, normally floating, `canJoinAllSpaces`, `fullScreenAuxiliary` and `canJoinAllApplications`; the last flag opts into joining other applications' fullscreen spaces when eligible, not a visibility/focus guarantee. |
| Transport | Fixed GET `http://127.0.0.1:8080/api/dashboard`, matching `Origin: http://127.0.0.1:8080`; sequential requests, next poll two seconds after completion. |
| HTTP limits | 3-second request / 5-second resource timeout, 1 MiB decoded-body cap while streaming, HTTP 200 JSON only. All redirects (including same loopback), authentication challenges, other origins/statuses and oversized bodies fail closed. |
| Local isolation | Ephemeral session, no proxy, cookie store, credential store or URL cache; no configuration flags, persistence or raw payload/error logging. Only needed dashboard fields are decoded, not player identity. |
| Freshness | UI reevaluates independently every 250 ms, including while a request hangs. `last_success_at` must not be future-dated and must be less than ten seconds old. HTTP success, dashboard `updated_at` and cached snapshots alone never mean live telemetry. |
| Advice | Only fresh `in_game` / `connected` telemetry with an active session, supported map/mode, ready matching catalog patch, exact source-game revision, and `ready` or `save_for_component` plus valid target/component. Other states clear the previous advice. Warnings are bounded to three 200-character lines; the small panel can clip long copy. |

**Conservative provenance limitation:** `source_game_revision` must equal `game.revision`; passive time/gold ticks legitimately advance the game without recalculating advice. This spike pauses in that case, potentially until the coordinator's periodic recalculation. It does not invent a session binding for older recommendations. `catalog.source: cache` is not a telemetry freshness signal, nor is catalog patch proof of installed game version. Successful telemetry polls can update the tracker without publishing a new dashboard; retained dashboards can therefore conservatively expire despite a healthy tracker. Neither behavior changes the server.

## Compiler/test evidence versus observation

Host evidence supplied for this task: macOS **27.0.1**, build **26A434**, arm64; Swift **6.4.2**, installed Xcode SDK metadata **27.2**. The earlier `xcrun --show-sdk-version` lookup failed; it was not retried or repaired. Explicit SDK compilation and synthetic policy/HTTP delegate tests succeeded. Tests cover clearing advice, local expiry without completion, Go fractional timestamps/integer fields, ready/save behavior, incoherence and redirect denial; the original checks do not connect to a server or establish AppKit focus, Spaces or League behavior. A separate verifier passed 26 additional synthetic policy checks and 16 real URLSession exchanges against an exclusively owned synthetic loopback server: origin/path/header checks, redirect/auth denial, proxy/cookie isolation, exact/oversized streaming bodies, and stalled/truncated responses. Its redirect/proxy trap received zero connections. These are native wire observations, not game observations.

Observed verification: Swift tests reported 41 passes / `0 failure(s)`, including exact exit/stdout/stderr checks for default/aliased help and invalid/extra arguments, and the overlay build succeeded without launching. These regression commands also passed (two Go packages, 23 Node tests, clean tracked diff):

```bash
go test ./internal/api ./internal/webui
node --test internal/webui/state_test.js internal/webui/reconnect_test.js
git diff --check
```

The native consumer sends the existing exact local Origin and leaves Go loopback/Host/Origin/CSP protections unchanged. API/webui Go and browser-state Node checks are separate regression checks, not native/browser equivalence. **Native requests do not exercise browser CSP; browser compatibility is not established by this spike.** A different process on the fixed loopback port remains a trust limitation. Risk item 2 (new native transport boundary) received independent verification with no candidate-caused blocker. The verifier's API execution was unavailable because its isolated offline cache lacked dependencies; the writer's targeted API tests and parent's full `go test ./...` passed. Parent spot checks also passed Swift tests/build and all 35 Node tests.

## Practice Tool observations

Tested on the main display of an arm64 Mac running macOS 27.0.1 (26A434), League game bundle 16.23.8396405. A second 1920 × 1080 display was connected to the left. Stage Manager and Spaces settings were not recorded.

| Game video mode | Main display bounds (Quartz) | Panel above game | Mouse reaches game through panel | Controls |
| --- | --- | --- | --- | --- |
| Pantalla completa | 1920 × 1200 | Confirmed by player | Confirmed by player | Player confirmed Hide, recovery via RW, and keyboard/mouse back in game |
| Sin bordes | 1728 × 1117 | Confirmed by player | Confirmed by player | Player confirmed Hide, recovery via RW, and keyboard/mouse back in game |

In the earlier floating-level build the player saw the panel and RW menu on the desktop and the panel in windowed League. In Fullscreen it disappeared, including after the addition of `canJoinAllApplications`. An initial name-filtered diagnostic returned windows belonging to the launcher/client and missed the foreground gameplay windows.

### Foreground game window ordering, 18 November 2026

On macOS 27.0.1 (26A434), arm64, League game bundle 16.23.8396405, a public WindowServer query matched the foreground gameplay **PID** from `com.riotgames.LeagueofLegends.GameClient`. On the main 1920 × 1200 display it returned two display-sized game windows at level 2147483628, both ahead of the original level-3 panel. Matching the window owner by its display name had missed both. The display's shielding window ID was zero. The numeric level alone does not establish exclusive capture.

The candidate now checks the foreground application's bundle, then uses the frontmost on-screen window of that PID whose Quartz bounds equal the panel's display. It uses the greater of floating or that window's level plus one, provided that level stays below the cursor. It falls back to floating on app switch, a different display, missing metadata or an unsupported level. App/Space changes refresh the level, display changes reapply placement, and a visible panel checks once a second to pick up video-mode changes within the game. The display-size check applies to both game modes without reading or changing League settings.

After rebuilding and replacing the old panel, a second sample found the panel first (2147483629, 360 × 210, fully inside the main display) followed by both game windows (2147483628). In Borderless the panel was first at level 1001 followed by the display-sized game window at 1000. League remained foreground during both samples. The build, 45 Swift checks, API/web UI Go tests and 35 Node tests passed. The running backend reported connected telemetry and an unavailable generic profile for catalog patch 16.23.1, and the native consumer returned “API connected • telemetry fresh • advice unavailable — advice paused” with empty advice. Actionable advice, stale expiry and disconnect clearing are covered by fixtures. [Apple documents that compositing over fullscreen graphics can vary with the renderer and hardware.](https://developer.apple.com/documentation/coregraphics/cgshieldingwindowlevel%28%29)

**Feasibility decision.** Proceed with an external AppKit panel for these two tested modes on this configuration. Renderer, display topology and OS changes warrant a new check. Secondary-screen gameplay, other hardware and distribution remain unvalidated.

## AppKit API references

- [NSPanel](https://developer.apple.com/documentation/appkit/nspanel)
- [nonactivatingPanel](https://developer.apple.com/documentation/appkit/nswindow/stylemask-swift.struct/nonactivatingpanel)
- [fullScreenAuxiliary](https://developer.apple.com/documentation/appkit/nswindow/collectionbehavior-swift.struct/fullscreenauxiliary)
- [ignoresMouseEvents](https://developer.apple.com/documentation/appkit/nswindow/ignoresmouseevents)
