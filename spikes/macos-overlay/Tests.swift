import Foundation

@main
struct Tests {
    static func main() {
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        let fresh = "2027-01-15T07:59:59.123456789Z"
        var failures = 0
        func check(_ condition: Bool, _ name: String) {
            print("\(condition ? "PASS" : "FAIL") \(name)")
            if !condition { failures += 1 }
        }
        func payload(_ game: [String: Any] = [:], _ rec: [String: Any] = [:],
                     _ catalog: [String: Any] = [:]) -> Data {
            var g: [String: Any] = ["status": "in_game", "connection": "connected", "revision": 7,
                "last_success_at": fresh, "session": ["id": "synthetic", "state": "active"],
                "game": ["map_number": 11, "game_mode": "PRACTICETOOL"]]
            var r: [String: Any] = ["status": "ready", "source_game_revision": 7,
                "generated_at": fresh, "patch": "16.22.1", "gold_needed": 0,
                "target_item": ["id": 3000, "name": "Target", "cost": 3000],
                "next_component": ["id": 1000, "name": "Component", "cost": 500]]
            var c: [String: Any] = ["ready": true, "patch": "16.22.1", "source": "cache"]
            g.merge(game) { _, new in new }
            r.merge(rec) { _, new in new }
            c.merge(catalog) { _, new in new }
            return try! JSONSerialization.data(withJSONObject: ["epoch": "test", "revision": 9,
                "game": g, "recommendation": r, "catalog": c])
        }
        func view(_ data: Data, at: Date? = nil) -> Presentation {
            var policy = OverlayPolicy()
            policy.receive(data)
            return policy.present(now: at ?? now)
        }
        let ready = view(payload())
        check(ready.advice == "Compare: Target\nComponent: Component (500 gold)" &&
              ready.status == "API connected • telemetry fresh • catalog cache", "fresh actionable / Go fractional timestamp / cached catalog")
        check(view(payload([:], ["status": "save_for_component", "gold_needed": 200])).advice ==
              "Compare: Target\nSave: Component (200 gold needed)", "preserve save_for_component and integer gold_needed")
        var policy = OverlayPolicy()
        policy.receive(payload())
        check(policy.present(now: now.addingTimeInterval(10)).advice.isEmpty &&
              policy.present(now: now.addingTimeInterval(10)).status == "API connected • telemetry stale — advice paused" &&
              view(payload(["last_success_at": "2027-01-15T07:59:50Z"])).advice.isEmpty,
              "last_success_at expires at 10s even with no request completion (hung request)")
        policy.disconnect()
        check(policy.present(now: now).advice.isEmpty && policy.present(now: now).status ==
              "API disconnected — advice paused", "transport failure clears prior advice")
        policy.receive(payload())
        policy.receive(Data("{invalid".utf8))
        check(policy.present(now: now) == Presentation(status: "API invalid — advice paused", advice: ""),
              "invalid response clears prior advice")
        for status in ["waiting", "stale", "unsupported", "catalog_unavailable", "inventory_full", "complete", "insufficient_context", "error", "unknown"] {
            let paused = view(payload([:], ["status": status]))
            check(paused.advice.isEmpty && paused.status.contains("advice paused"), "nonactionable \(status) clears advice")
        }
        for (name, data) in [
            ("stale tracker", payload(["status": "stale"])),
            ("disconnected telemetry", payload(["connection": "reconnecting"])),
            ("ended", payload(["session": ["id": "synthetic", "state": "ended"]])),
            ("missing game", payload(["game": NSNull()])),
            ("missing timestamp", payload(["last_success_at": NSNull()])),
            ("future telemetry", payload(["last_success_at": "2027-01-15T08:00:01Z"])),
            ("wrong revision", payload([:], ["source_game_revision": 6])),
            ("future revision", payload([:], ["source_game_revision": 8])),
            ("wrong patch", payload([:], ["patch": "16.21.1"])),
            ("catalog not ready", payload([:], [:], ["ready": false])),
            ("missing component", payload([:], ["next_component": NSNull()])),
            ("invalid item", payload([:], ["target_item": ["id": 0, "name": "", "cost": 0]])),
            ("wrong JSON type", payload([:], ["gold_needed": "200"]))
        ] {
            var p = OverlayPolicy()
            p.receive(payload())
            p.receive(data)
            let paused = p.present(now: now)
            check(paused.advice.isEmpty && paused.status.contains("advice paused"), "incoherent \(name) clears advice")
        }
        let request = DashboardTransport.request()
        let config = DashboardTransport.configuration()
        check(request.url == DashboardTransport.endpoint && request.httpMethod == "GET" &&
              request.value(forHTTPHeaderField: "Origin") == "http://127.0.0.1:8080" &&
              request.timeoutInterval == 3 && config.timeoutIntervalForResource == 5 &&
              config.urlCache == nil && config.httpCookieStorage == nil && config.urlCredentialStorage == nil &&
              config.httpShouldSetCookies == false && config.connectionProxyDictionary?.isEmpty == true,
              "fixed origin / matching Origin / bounded timeout / no proxy cookies credentials cache")
        func response(_ status: Int, url: URL = DashboardTransport.endpoint) -> HTTPURLResponse {
            HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1",
                             headerFields: ["Content-Type": "application/json"])!
        }
        check(DashboardTransport.accepts(response(200), bytes: DashboardTransport.limit) &&
              !DashboardTransport.accepts(response(200), bytes: DashboardTransport.limit + 1) &&
              !DashboardTransport.accepts(response(403), bytes: 0) &&
              !DashboardTransport.accepts(response(200, url: URL(string: "http://127.0.0.1:8081/api/dashboard")!), bytes: 0),
              "HTTP status origin and streaming size boundary")
        let transport = DashboardTransport()
        let session = URLSession(configuration: .ephemeral)
        let task = session.dataTask(with: request) // never resumed; no network
        for destination in ["http://127.0.0.1:8080/api/dashboard", "https://example.invalid/leak"] {
            var denied = false
            transport.urlSession(session, task: task, willPerformHTTPRedirection: response(302),
                newRequest: URLRequest(url: URL(string: destination)!)) { denied = $0 == nil }
            var rejectedBody = false
            transport.urlSession(session, dataTask: task, didReceive: response(200)) { rejectedBody = $0 == .cancel }
            check(denied && rejectedBody && !DashboardTransport.accepts(response(302), bytes: 0),
                  "redirect denied without forwarding or activating advice: \(destination)")
        }
        session.invalidateAndCancel()
        let usage = "Usage: bash spikes/macos-overlay/check.sh {test|build|help} (macOS arm64, Xcode SDK; never launches GUI)\n"
        let commandCases: [([String], Int32, String, String)] = [
            ([], 0, usage, ""),
            (["help"], 0, usage, ""),
            (["--help"], 0, usage, ""),
            (["-h"], 0, usage, ""),
            (["help", "build"], 2, "", "Expected exactly one argument\n"),
            (["--help", "build"], 2, "", "Expected exactly one argument\n"),
            (["-h", "build"], 2, "", "Expected exactly one argument\n"),
            (["test", "extra"], 2, "", "Expected exactly one argument\n"),
            (["build", "extra"], 2, "", "Expected exactly one argument\n"),
            (["invalid"], 2, "", "Expected test, build or help\n")
        ]
        for (arguments, status, stdout, stderr) in commandCases {
            let process = Process()
            process.executableURL = URL(fileURLWithPath: "/bin/bash")
            process.arguments = ["check.sh"] + arguments
            let output = Pipe()
            let error = Pipe()
            process.standardOutput = output
            process.standardError = error
            do {
                try process.run()
                process.waitUntilExit()
                let actualOutput = output.fileHandleForReading.readDataToEndOfFile()
                let actualError = error.fileHandleForReading.readDataToEndOfFile()
                check(process.terminationStatus == status && actualOutput == Data(stdout.utf8) &&
                      actualError == Data(stderr.utf8), "check.sh \(arguments) exact exit/stdout/stderr")
            } catch {
                check(false, "check.sh \(arguments) launch failed: \(error)")
            }
        }
        print("\(failures) failure(s)")
        if failures > 0 { exit(1) }
    }
}
