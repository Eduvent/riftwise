import AppKit

enum GameWindowLevel {
    // The match and the launcher are different applications.
    static let bundleID = "com.riotgames.LeagueofLegends.GameClient"

    static func current(on screen: NSScreen?) -> NSWindow.Level {
        guard let game = NSWorkspace.shared.frontmostApplication, game.bundleIdentifier == bundleID,
              let displayID = screen?.deviceDescription[NSDeviceDescriptionKey("NSScreenNumber")] as? NSNumber,
              let windows = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements],
                                                       kCGNullWindowID) as? [[String: Any]] else {
            return .floating
        }
        return level(for: game.processIdentifier, display: CGDisplayBounds(displayID.uint32Value), windows: windows)
    }

    static func level(for pid: pid_t, display: CGRect, windows: [[String: Any]]) -> NSWindow.Level {
        let floating = NSWindow.Level.floating.rawValue
        let cursor = Int(CGWindowLevelForKey(.cursorWindow))
        guard !display.isEmpty else { return .floating }
        // WindowServer lists front to back. Compare Quartz display/window bounds in the same coordinate space.
        for window in windows {
            guard window[kCGWindowOwnerPID as String] as? pid_t == pid,
                  let bounds = window[kCGWindowBounds as String] as? [String: Any],
                  CGRect(dictionaryRepresentation: bounds as CFDictionary) == display,
                  let layer = window[kCGWindowLayer as String] as? Int else { continue }
            guard layer < cursor - 1 else { return .floating }
            return NSWindow.Level(rawValue: max(floating, layer + 1))
        }
        return .floating
    }
}
