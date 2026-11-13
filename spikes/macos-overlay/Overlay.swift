import AppKit

private final class OverlayPanel: NSPanel {
    override var canBecomeKey: Bool { false }
    override var canBecomeMain: Bool { false }
}

@main
final class Overlay: NSObject, NSApplicationDelegate {
    private let transport = DashboardTransport()
    private var policy = OverlayPolicy()
    private var pending = false
    private var nextPoll = Date.distantPast
    private var timer: Timer?
    private var item: NSStatusItem!
    private var panel: OverlayPanel!
    private let status = NSTextField(wrappingLabelWithString: "")
    private let advice = NSTextField(wrappingLabelWithString: "")
    private var screenIndex = 0
    private var right = true

    static func main() {
        let app = NSApplication.shared
        let delegate = Overlay()
        app.delegate = delegate
        app.setActivationPolicy(.accessory)
        withExtendedLifetime(delegate) { app.run() }
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        panel = OverlayPanel(contentRect: NSRect(x: 0, y: 0, width: 360, height: 210),
                             styleMask: [.borderless, .nonactivatingPanel], backing: .buffered, defer: false)
        panel.level = .floating
        panel.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary]
        panel.hidesOnDeactivate = false
        panel.ignoresMouseEvents = true
        panel.isMovableByWindowBackground = true
        panel.backgroundColor = .windowBackgroundColor
        status.frame = NSRect(x: 16, y: 148, width: 328, height: 48)
        status.font = .systemFont(ofSize: 13, weight: .semibold)
        advice.frame = NSRect(x: 16, y: 40, width: 328, height: 104)
        advice.font = .systemFont(ofSize: 14)
        let hide = NSButton(title: "Hide (controls: RW menu)", target: self, action: #selector(toggleVisible))
        hide.frame = NSRect(x: 12, y: 6, width: 300, height: 28)
        for view in [status, advice, hide] { panel.contentView?.addSubview(view) }
        item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        item.button?.title = "RW"
        let menu = NSMenu()
        add("Show / Hide overlay", #selector(toggleVisible), to: menu)
        add("Toggle interaction (default: click-through)", #selector(toggleInteraction), to: menu)
        add("Placement: next screen", #selector(nextScreen), to: menu)
        add("Placement: left / right top corner", #selector(toggleCorner), to: menu)
        add("Quit overlay", #selector(quit), to: menu)
        item.menu = menu
        place()
        panel.orderFrontRegardless()
        let timer = Timer(timeInterval: 0.25, repeats: true) { [weak self] _ in self?.tick() }
        self.timer = timer
        RunLoop.main.add(timer, forMode: .common)
        tick()
    }

    private func add(_ title: String, _ action: Selector, to menu: NSMenu) {
        let entry = NSMenuItem(title: title, action: action, keyEquivalent: "")
        entry.target = self
        menu.addItem(entry)
    }

    private func tick() {
        let view = policy.present(now: Date())
        status.stringValue = view.status
        advice.stringValue = view.advice
        panel.title = panel.ignoresMouseEvents ? "Click-through ON" : "Interaction ON"
        item.button?.toolTip = "Riftwise spike • \(panel.title)"
        guard !pending, Date() >= nextPoll else { return }
        pending = true
        transport.fetch { [weak self] data in
            guard let self else { return }
            if let data { self.policy.receive(data) } else { self.policy.disconnect() }
            self.pending = false
            self.nextPoll = Date().addingTimeInterval(2)
            self.tick()
        }
    }

    private func place() {
        let screens = NSScreen.screens
        guard !screens.isEmpty else { return }
        screenIndex %= screens.count
        let frame = screens[screenIndex].visibleFrame
        let x = right ? frame.maxX - panel.frame.width - 16 : frame.minX + 16
        panel.setFrameOrigin(NSPoint(x: max(frame.minX, x), y: max(frame.minY, frame.maxY - panel.frame.height - 16)))
    }

    @objc private func toggleVisible() {
        if panel.isVisible { panel.orderOut(nil) } else { place(); panel.orderFrontRegardless() }
    }
    @objc private func toggleInteraction() { panel.ignoresMouseEvents.toggle() }
    @objc private func nextScreen() { screenIndex += 1; place() }
    @objc private func toggleCorner() { right.toggle(); place() }
    @objc private func quit() { NSApplication.shared.terminate(nil) }
    func applicationWillTerminate(_ notification: Notification) { timer?.invalidate(); transport.stop() }
}
