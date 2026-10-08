import SwiftUI
import AppKit
import SentinelCore
import SentinelMediaServer

// MARK: - Result Model

struct CommandResult: Identifiable {
    enum Category: String {
        case section = "Section"
        case camera = "Camera"
        case alert = "Alert"
        case action = "Action"

        var tint: Color {
            switch self {
            case .section: return SentinelTheme.accent
            case .camera: return SentinelTheme.recording
            case .alert: return SentinelTheme.alarm
            case .action: return SentinelTheme.amber
            }
        }
    }

    let id: UUID
    let icon: String
    let primary: String
    let secondary: String
    let category: Category
    let action: () -> Void
}

// MARK: - Store

@MainActor
final class CommandPaletteStore: ObservableObject {
    @Published var isOpen: Bool = false
    @Published var query: String = ""
    @Published var selectedIndex: Int = 0

    // Most-recent cached results so `activate()` can run the highlighted item.
    private var currentResults: [CommandResult] = []

    func open() {
        query = ""
        selectedIndex = 0
        isOpen = true
    }

    func close() {
        isOpen = false
        query = ""
        selectedIndex = 0
    }

    func activate() {
        guard currentResults.indices.contains(selectedIndex) else {
            close()
            return
        }
        let item = currentResults[selectedIndex]
        close()
        item.action()
    }

    /// Compute results for the current query against the supplied inputs.
    /// Also caches the result list internally so `activate()` can fire the selection.
    func results(
        cameras: [CameraFeed],
        alerts: [AlertEvent],
        sections: [SentinelSection],
        sectionAction: @escaping (SentinelSection) -> Void = { _ in },
        cameraAction: @escaping (CameraFeed) -> Void = { _ in },
        alertAction: @escaping (AlertEvent) -> Void = { _ in }
    ) -> [CommandResult] {
        var candidates: [(score: Int, result: CommandResult)] = []

        let q = query.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()

        for section in sections {
            let primary = section.rawValue
            let secondary = "Section"
            let score = matchScore(query: q, primary: primary, secondary: secondary)
            guard score > 0 || q.isEmpty else { continue }
            let result = CommandResult(
                id: UUID(),
                icon: section.symbol,
                primary: primary,
                secondary: secondary,
                category: .section,
                action: { sectionAction(section) }
            )
            candidates.append((q.isEmpty ? 1 : score, result))
        }

        for camera in cameras {
            let primary = camera.name
            let secondary = "Camera \u{00B7} \(camera.location)"
            let score = matchScore(query: q, primary: primary, secondary: secondary)
            guard score > 0 || q.isEmpty else { continue }
            let result = CommandResult(
                id: camera.id,
                icon: "video.fill",
                primary: primary,
                secondary: secondary,
                category: .camera,
                action: { cameraAction(camera) }
            )
            candidates.append((q.isEmpty ? 1 : score, result))
        }

        for alert in alerts {
            let primary = alert.title
            let cam = alert.cameraName ?? alert.source
            let secondary = "Alert \u{00B7} \(cam)"
            let score = matchScore(query: q, primary: primary, secondary: secondary)
            guard score > 0 || q.isEmpty else { continue }
            let result = CommandResult(
                id: alert.id,
                icon: "exclamationmark.triangle.fill",
                primary: primary,
                secondary: secondary,
                category: .alert,
                action: { alertAction(alert) }
            )
            candidates.append((q.isEmpty ? 1 : score, result))
        }

        let sorted = candidates.sorted { $0.score > $1.score }
        let top = Array(sorted.prefix(30)).map(\.result)
        currentResults = top
        if selectedIndex >= top.count {
            selectedIndex = max(0, top.count - 1)
        }
        return top
    }

    // MARK: - Fuzzy scoring

    private func matchScore(query q: String, primary: String, secondary: String) -> Int {
        guard q.isEmpty == false else { return 0 }
        let primaryLower = primary.lowercased()
        let secondaryLower = secondary.lowercased()

        if primaryLower.hasPrefix(q) { return 100 }
        if secondaryLower.hasPrefix(q) { return 90 }

        let primaryWords = primaryLower.split(whereSeparator: { !$0.isLetter && !$0.isNumber })
        if primaryWords.contains(where: { $0.hasPrefix(q) }) { return 50 }
        let secondaryWords = secondaryLower.split(whereSeparator: { !$0.isLetter && !$0.isNumber })
        if secondaryWords.contains(where: { $0.hasPrefix(q) }) { return 40 }

        if primaryLower.contains(q) { return 20 }
        if secondaryLower.contains(q) { return 15 }

        if Self.isSubsequence(q, of: primaryLower) { return 10 }
        if Self.isSubsequence(q, of: secondaryLower) { return 5 }

        return 0
    }

    private static func isSubsequence(_ needle: String, of haystack: String) -> Bool {
        var iter = haystack.makeIterator()
        for ch in needle {
            var matched = false
            while let next = iter.next() {
                if next == ch {
                    matched = true
                    break
                }
            }
            if matched == false { return false }
        }
        return true
    }
}

// MARK: - View

struct CommandPaletteView: View {
    @ObservedObject var store: CommandPaletteStore
    let cameras: [CameraFeed]
    let alerts: [AlertEvent]
    let onSelectSection: (SentinelSection) -> Void
    let onSelectCamera: (CameraFeed) -> Void
    let onSelectAlert: (AlertEvent) -> Void

    @FocusState private var queryFocused: Bool

    private var results: [CommandResult] {
        store.results(
            cameras: cameras,
            alerts: alerts,
            sections: SentinelSection.allCases,
            sectionAction: onSelectSection,
            cameraAction: onSelectCamera,
            alertAction: onSelectAlert
        )
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Image(systemName: "magnifyingglass")
                    .foregroundStyle(SentinelTheme.accent)
                TextField("Search cameras, sections, alerts\u{2026}", text: $store.query)
                    .textFieldStyle(.plain)
                    .font(.system(size: 17, weight: .regular))
                    .focused($queryFocused)
                    .onSubmit { store.activate() }
                if store.query.isEmpty == false {
                    Button {
                        store.query = ""
                    } label: {
                        Image(systemName: "xmark.circle.fill")
                            .foregroundStyle(.secondary)
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 14)

            Divider().overlay(SentinelTheme.line)

            let items = results

            if items.isEmpty {
                VStack(spacing: 8) {
                    Image(systemName: "questionmark.bubble")
                        .font(.system(size: 28))
                        .foregroundStyle(.secondary)
                    Text(store.query.isEmpty ? "Type to search" : "No results")
                        .foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 40)
            } else {
                ScrollViewReader { proxy in
                    List(Array(items.enumerated()), id: \.element.id) { index, item in
                        CommandPaletteRow(
                            item: item,
                            isSelected: index == store.selectedIndex
                        )
                        .id(item.id)
                        .listRowInsets(EdgeInsets(top: 4, leading: 8, bottom: 4, trailing: 8))
                        .listRowSeparator(.hidden)
                        .listRowBackground(Color.clear)
                        .contentShape(Rectangle())
                        .onTapGesture {
                            store.selectedIndex = index
                            store.activate()
                        }
                    }
                    .listStyle(.plain)
                    .scrollContentBackground(.hidden)
                    .onChange(of: store.selectedIndex) { newValue in
                        guard items.indices.contains(newValue) else { return }
                        withAnimation(.easeInOut(duration: 0.12)) {
                            proxy.scrollTo(items[newValue].id, anchor: .center)
                        }
                    }
                }
            }

            Divider().overlay(SentinelTheme.line)

            HStack(spacing: 12) {
                hintLabel(symbol: "arrow.up.arrow.down", text: "Navigate")
                hintLabel(symbol: "return", text: "Open")
                hintLabel(symbol: "escape", text: "Close")
                Spacer()
                Text("\(items.count) result\(items.count == 1 ? "" : "s")")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 10)
        }
        .frame(width: 640, height: 480)
        .background(SentinelTheme.panelRaised)
        .overlay(
            RoundedRectangle(cornerRadius: 12)
                .stroke(SentinelTheme.line, lineWidth: 1)
        )
        .clipShape(RoundedRectangle(cornerRadius: 12))
        .background(KeyHandler(
            onUp: {
                let count = results.count
                guard count > 0 else { return }
                store.selectedIndex = (store.selectedIndex - 1 + count) % count
            },
            onDown: {
                let count = results.count
                guard count > 0 else { return }
                store.selectedIndex = (store.selectedIndex + 1) % count
            },
            onEnter: { store.activate() },
            onEscape: { store.close() }
        ))
        .onAppear {
            queryFocused = true
            store.selectedIndex = 0
        }
        .onChange(of: store.query) { _ in
            store.selectedIndex = 0
        }
    }

    private func hintLabel(symbol: String, text: String) -> some View {
        HStack(spacing: 4) {
            Image(systemName: symbol)
                .font(.system(size: 11, weight: .medium))
            Text(text)
                .font(.caption)
        }
        .foregroundStyle(.secondary)
    }
}

private struct CommandPaletteRow: View {
    let item: CommandResult
    let isSelected: Bool

    var body: some View {
        HStack(spacing: 12) {
            ZStack {
                RoundedRectangle(cornerRadius: 7)
                    .fill(item.category.tint.opacity(0.18))
                Image(systemName: item.icon)
                    .font(.system(size: 14, weight: .semibold))
                    .foregroundStyle(item.category.tint)
            }
            .frame(width: 30, height: 30)

            VStack(alignment: .leading, spacing: 2) {
                Text(item.primary)
                    .font(.system(size: 14, weight: .medium))
                    .lineLimit(1)
                Text(item.secondary)
                    .font(.system(size: 12))
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }

            Spacer()

            Text(item.category.rawValue.uppercased())
                .font(.system(size: 9, weight: .heavy))
                .tracking(0.8)
                .foregroundStyle(item.category.tint)
                .padding(.horizontal, 6)
                .padding(.vertical, 3)
                .background(
                    RoundedRectangle(cornerRadius: 4)
                        .fill(item.category.tint.opacity(0.16))
                )
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
        .background(
            RoundedRectangle(cornerRadius: 8)
                .fill(isSelected ? SentinelTheme.accent.opacity(0.22) : Color.clear)
        )
        .overlay(
            RoundedRectangle(cornerRadius: 8)
                .stroke(isSelected ? SentinelTheme.accent.opacity(0.6) : Color.clear, lineWidth: 1)
        )
    }
}

// MARK: - Key handler (NSView bridge)

private struct KeyHandler: NSViewRepresentable {
    let onUp: () -> Void
    let onDown: () -> Void
    let onEnter: () -> Void
    let onEscape: () -> Void

    func makeNSView(context: Context) -> KeyHandlerView {
        let view = KeyHandlerView()
        view.onUp = onUp
        view.onDown = onDown
        view.onEnter = onEnter
        view.onEscape = onEscape
        return view
    }

    func updateNSView(_ nsView: KeyHandlerView, context: Context) {
        nsView.onUp = onUp
        nsView.onDown = onDown
        nsView.onEnter = onEnter
        nsView.onEscape = onEscape
    }
}

final class KeyHandlerView: NSView {
    var onUp: (() -> Void)?
    var onDown: (() -> Void)?
    var onEnter: (() -> Void)?
    var onEscape: (() -> Void)?

    private var monitor: Any?

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        if window != nil {
            installMonitor()
        } else {
            removeMonitor()
        }
    }

    private func installMonitor() {
        removeMonitor()
        monitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { [weak self] event in
            guard let self, let window = self.window, event.window === window else {
                return event
            }
            switch event.keyCode {
            case 126: // up
                self.onUp?()
                return nil
            case 125: // down
                self.onDown?()
                return nil
            case 36, 76: // return / numpad enter
                self.onEnter?()
                return nil
            case 53: // escape
                self.onEscape?()
                return nil
            default:
                return event
            }
        }
    }

    private func removeMonitor() {
        if let monitor {
            NSEvent.removeMonitor(monitor)
            self.monitor = nil
        }
    }

    deinit {
        if let monitor {
            NSEvent.removeMonitor(monitor)
        }
    }
}

// MARK: - View modifier

extension View {
    func commandPalette(
        store: CommandPaletteStore,
        cameras: [CameraFeed],
        alerts: [AlertEvent],
        onSelectSection: @escaping (SentinelSection) -> Void,
        onSelectCamera: @escaping (CameraFeed) -> Void,
        onSelectAlert: @escaping (AlertEvent) -> Void
    ) -> some View {
        modifier(CommandPaletteModifier(
            store: store,
            cameras: cameras,
            alerts: alerts,
            onSelectSection: onSelectSection,
            onSelectCamera: onSelectCamera,
            onSelectAlert: onSelectAlert
        ))
    }
}

private struct CommandPaletteModifier: ViewModifier {
    @ObservedObject var store: CommandPaletteStore
    let cameras: [CameraFeed]
    let alerts: [AlertEvent]
    let onSelectSection: (SentinelSection) -> Void
    let onSelectCamera: (CameraFeed) -> Void
    let onSelectAlert: (AlertEvent) -> Void

    func body(content: Content) -> some View {
        content
            .background(
                Button(action: { store.open() }) {
                    EmptyView()
                }
                .keyboardShortcut("k", modifiers: .command)
                .opacity(0)
                .frame(width: 0, height: 0)
                .accessibilityHidden(true)
            )
            .sheet(isPresented: $store.isOpen) {
                CommandPaletteView(
                    store: store,
                    cameras: cameras,
                    alerts: alerts,
                    onSelectSection: onSelectSection,
                    onSelectCamera: onSelectCamera,
                    onSelectAlert: onSelectAlert
                )
            }
    }
}
