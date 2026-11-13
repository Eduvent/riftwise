import Foundation

// An allowlist projection: no player identity, history, or raw payload retained.
private struct Dashboard: Decodable {
    struct Game: Decodable {
        struct Session: Decodable {
            let id: String
            let state: String
        }
        struct Match: Decodable {
            let mapNumber: Int
            let gameMode: String
        }
        let status: String
        let connection: String
        let revision: UInt64
        let lastSuccessAt: Date?
        let lastError: String?
        let session: Session?
        let game: Match?
    }
    struct Catalog: Decodable {
        let ready: Bool
        let patch: String
        let source: String
    }
    struct Recommendation: Decodable {
        struct Item: Decodable {
            let id: Int
            let name: String
            let cost: Int
            var valid: Bool {
                id > 0 && (1...100_000).contains(cost) && !name.trimmingCharacters(in: .whitespaces).isEmpty &&
                name.count <= 120 && name.rangeOfCharacter(from: .controlCharacters) == nil
            }
        }
        let status: String
        let sourceGameRevision: UInt64
        let generatedAt: Date
        let patch: String?
        let goldNeeded: Int
        let targetItem: Item?
        let nextComponent: Item?
        let warnings: [String]?
    }
    let epoch: String
    let revision: UInt64
    let game: Game
    let catalog: Catalog
    let recommendation: Recommendation
}

struct Presentation: Equatable {
    let status: String
    let advice: String
}

struct OverlayPolicy {
    private var dashboard: Dashboard?
    private var connection = "disconnected"

    mutating func receive(_ data: Data) {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        decoder.dateDecodingStrategy = .custom { decoder in
            let value = try decoder.singleValueContainer().decode(String.self)
            let formatter = ISO8601DateFormatter()
            formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let date = formatter.date(from: value) { return date }
            formatter.formatOptions = [.withInternetDateTime]
            guard let date = formatter.date(from: value) else { throw CocoaError(.coderReadCorrupt) }
            return date
        }
        dashboard = try? decoder.decode(Dashboard.self, from: data)
        connection = dashboard == nil ? "invalid" : "connected"
    }

    mutating func disconnect() {
        dashboard = nil
        connection = "disconnected"
    }

    func present(now: Date) -> Presentation {
        func pause(_ status: String) -> Presentation {
            Presentation(status: status + " — advice paused", advice: "")
        }
        guard let d = dashboard else { return pause("API \(connection)") }
        let g = d.game, r = d.recommendation
        guard let success = g.lastSuccessAt, now >= success else {
            return pause("API connected • telemetry unavailable")
        }
        // Independent of response arrival: the UI calls this on its own timer.
        guard now.timeIntervalSince(success) < 10 else { return pause("API connected • telemetry stale") }
        guard g.status == "in_game", g.connection == "connected", (g.lastError ?? "").isEmpty,
              g.session?.state == "active", !(g.session?.id ?? "").isEmpty,
              g.game?.mapNumber == 11, ["CLASSIC", "PRACTICETOOL"].contains(g.game?.gameMode ?? "") else {
            return pause("API connected • telemetry unavailable")
        }
        // Deliberately conservative: passive ticks can outpace recommendation revisions.
        // Pause rather than infer provenance that the current API does not expose.
        guard !d.epoch.isEmpty, d.revision > 0, g.revision > 0,
              r.sourceGameRevision == g.revision, r.generatedAt <= now,
              d.catalog.ready, !(r.patch ?? "").isEmpty, r.patch == d.catalog.patch,
              ["ready", "save_for_component"].contains(r.status),
              let target = r.targetItem, target.valid,
              let component = r.nextComponent, component.valid,
              (0...100_000).contains(r.goldNeeded),
              (r.status == "ready" ? r.goldNeeded == 0 : r.goldNeeded > 0) else {
            return pause("API connected • telemetry fresh • advice unavailable")
        }
        let action = r.status == "ready" ? "Component: \(component.name) (\(component.cost) gold)" :
            "Save: \(component.name) (\(r.goldNeeded) gold needed)"
        let warnings = (r.warnings ?? []).prefix(3).map { String($0.prefix(200)) }
        return Presentation(status: "API connected • telemetry fresh • catalog \(String(d.catalog.source.prefix(40)))",
                            advice: (["Compare: \(target.name)", action] + warnings).joined(separator: "\n"))
    }
}
