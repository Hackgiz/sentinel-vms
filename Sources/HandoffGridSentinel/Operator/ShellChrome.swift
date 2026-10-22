import AppKit
import SwiftUI
import SentinelCore

/// The view tab strip inside the Live workspace (wave 20: the fixed
/// workspaces moved to the rail): the Live grid, closable saved-view tabs,
/// a "+" view picker and, on the right, the setup-mode pencil.
struct ShellTabBar: View {
    @ObservedObject var library: ViewLibrary
    let tabs: TabsState
    let setup: Bool
    let onSelect: (String) -> Void
    let onCloseView: (String) -> Void
    let onOpenView: (String) -> Void
    let onSetup: () -> Void

    @State private var picking = false

    var body: some View {
        HStack(spacing: 8) {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 2) {
                    liveTab
                    ForEach(tabs.views, id: \.self) { id in viewTab(id) }
                    plusButton
                }
                .padding(.vertical, 3)
            }
            Spacer(minLength: 8)
            Button(action: onSetup) {
                Label("Setup", systemImage: "pencil")
                    .labelStyle(.titleAndIcon)
            }
            .buttonStyle(.bordered)
            .tint(setup ? SentinelTheme.accent : nil)
            .controlSize(.small)
            .help(setup ? "Leave setup mode" : "Setup mode: edit the open view")
            .accessibilityLabel("Setup mode")
            .accessibilityValue(setup ? "On" : "Off")
        }
        .padding(.horizontal, SentinelTheme.Space.lg)
        .frame(height: 34)
        .background(SentinelTheme.chrome)
    }

    private var liveTab: some View {
        let selected = tabs.active == FixedTab.live.rawValue
        return Button { onSelect(FixedTab.live.rawValue) } label: {
            HStack(spacing: 5) {
                Image(systemName: "square.grid.2x2.fill").font(.system(size: 10))
                Text("Live grid")
            }
        }
        .buttonStyle(TabButtonStyle(selected: selected))
        .help("The Live grid (⌘1)")
    }

    private func viewTab(_ id: String) -> some View {
        let tab = TabsState.viewTabID(id)
        let selected = tabs.active == tab
        let name = library.view(id)?.name ?? "View"
        return HStack(spacing: 2) {
            Button { onSelect(tab) } label: {
                HStack(spacing: 5) {
                    Image(systemName: "rectangle.grid.2x2").font(.system(size: 10))
                    Text(name).lineLimit(1).frame(maxWidth: 140)
                }
            }
            .buttonStyle(TabButtonStyle(selected: selected, trailingPadding: 2))
            Button { onCloseView(id) } label: {
                Image(systemName: "xmark").font(.system(size: 8, weight: .bold))
                    .frame(width: 16, height: 16)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .foregroundStyle(.secondary)
            .help("Close tab")
            .accessibilityLabel("Close \(name)")
        }
        .background(selected ? SentinelTheme.accent.opacity(0.14) : Color.clear, in: RoundedRectangle(cornerRadius: 6))
        .help(name)
        .contextMenu {
            Button("Close Tab") { onCloseView(id) }
        }
    }

    private var plusButton: some View {
        Button { picking.toggle() } label: {
            Image(systemName: "plus").font(.system(size: 11, weight: .semibold)).frame(width: 22, height: 22)
        }
        .buttonStyle(.plain)
        .help("Open a view in a new tab")
        .accessibilityLabel("Open a view in a new tab")
        .popover(isPresented: $picking, arrowEdge: .bottom) {
            ViewPicker(library: library) { id in
                picking = false
                onOpenView(id)
            }
        }
    }
}

private struct TabButtonStyle: ButtonStyle {
    let selected: Bool
    var trailingPadding: CGFloat = 10

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.system(size: 12, weight: selected ? .semibold : .regular))
            .foregroundStyle(selected ? SentinelTheme.accent : Color.primary.opacity(0.78))
            .padding(.leading, 10).padding(.trailing, trailingPadding).padding(.vertical, 5)
            .background(
                RoundedRectangle(cornerRadius: 6)
                    .fill(selected ? SentinelTheme.accent.opacity(0.14) : (configuration.isPressed ? Color.primary.opacity(0.08) : Color.clear))
            )
            .contentShape(Rectangle())
    }
}

/// The "+" popover: find a saved view (private or shared) and open it in its own tab.
private struct ViewPicker: View {
    @ObservedObject var library: ViewLibrary
    let onPick: (String) -> Void
    @State private var query = ""
    @EnvironmentObject private var runtime: SiteRuntime

    private var entries: [(view: V2.SavedView, where: String)] {
        let all = library.privateViews.map { ($0, "Private") } + library.sharedViews.map { ($0, "Shared") }
        let needle = query.trimmingCharacters(in: .whitespaces).lowercased()
        return all.filter { needle.isEmpty || $0.0.name.lowercased().contains(needle) || $0.1.lowercased().contains(needle) }
            .map { (view: $0.0, where: $0.1) }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            TextField("Find a view", text: $query)
                .textFieldStyle(.roundedBorder)
            ScrollView {
                VStack(spacing: 0) {
                    ForEach(entries, id: \.view.id) { entry in
                        Button { onPick(entry.view.id) } label: {
                            HStack {
                                Text(entry.view.name).lineLimit(1)
                                Spacer(minLength: 12)
                                Text(entry.where).font(.caption).foregroundStyle(.secondary)
                            }
                            .padding(.vertical, 5).padding(.horizontal, 6)
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                    }
                    if entries.isEmpty {
                        Text(library.privateViews.isEmpty && library.sharedViews.isEmpty ? "No saved views yet." : "No views match.")
                            .font(.caption).foregroundStyle(.secondary)
                            .frame(maxWidth: .infinity, alignment: .leading).padding(6)
                    }
                }
            }
            .frame(maxHeight: 260)
        }
        .padding(12)
        .frame(width: 260)
        .task { await library.reload(api: runtime.api) }
    }
}

/// The wall clock; ticks on the minute.
struct WallClock: View {
    @State private var now = Date()

    var body: some View {
        Text(now.formatted(date: .omitted, time: .shortened))
            .font(.system(size: 12, weight: .medium).monospacedDigit())
            .foregroundStyle(.secondary)
            .help(now.formatted(date: .complete, time: .omitted))
            .task {
                while Task.isCancelled == false {
                    now = Date()
                    let wait = WorkspaceTabs.msToNextMinute(nowMs: Date().unixMillis)
                    try? await Task.sleep(nanoseconds: UInt64(wait) * 1_000_000)
                }
            }
    }
}

/// Full screen on / off for the window the shell lives in.
struct FullscreenToggle: View {
    let window: NSWindow?
    @State private var isFullScreen = false

    var body: some View {
        Button {
            window?.toggleFullScreen(nil)
        } label: {
            Image(systemName: isFullScreen ? "arrow.down.right.and.arrow.up.left" : "arrow.up.left.and.arrow.down.right")
        }
        .buttonStyle(.bordered)
        .disabled(window == nil)
        .help(isFullScreen ? "Leave full screen" : "Full screen")
        .accessibilityLabel(isFullScreen ? "Leave full screen" : "Full screen")
        .onAppear { isFullScreen = window?.styleMask.contains(.fullScreen) ?? false }
        .onReceive(NotificationCenter.default.publisher(for: NSWindow.didEnterFullScreenNotification)) { note in
            if (note.object as? NSWindow) === window { isFullScreen = true }
        }
        .onReceive(NotificationCenter.default.publisher(for: NSWindow.didExitFullScreenNotification)) { note in
            if (note.object as? NSWindow) === window { isFullScreen = false }
        }
    }
}

/// One slim, dismissible line per notice, under the toolbar. Fed by the
/// shared `NoticeCenter` (recorder / camera offline-and-back, the setup
/// hint, and anything the other workspaces post).
struct BannerStrip: View {
    @EnvironmentObject private var notices: NoticeCenter

    var body: some View {
        if notices.notices.isEmpty == false {
            VStack(spacing: 0) {
                ForEach(notices.notices) { notice in
                    HStack(spacing: 8) {
                        Image(systemName: symbol(notice.kind)).foregroundStyle(tint(notice.kind))
                        Text(notice.text).font(.callout).lineLimit(2).frame(maxWidth: .infinity, alignment: .leading)
                        Button { notices.dismiss(id: notice.id) } label: { Image(systemName: "xmark").font(.caption) }
                            .buttonStyle(.plain)
                            .foregroundStyle(.secondary)
                            .help("Dismiss")
                            .accessibilityLabel("Dismiss")
                    }
                    .padding(.horizontal, 14).padding(.vertical, 6)
                    .background(tint(notice.kind).opacity(0.12))
                    .overlay(alignment: .leading) { Rectangle().fill(tint(notice.kind)).frame(width: 3) }
                    .accessibilityElement(children: .combine)
                    .transition(.opacity)
                }
            }
            .animation(.easeInOut(duration: 0.15), value: notices.notices)
            .accessibilityElement(children: .contain)
            .accessibilityLabel("Notifications")
        }
    }

    private func symbol(_ kind: NoticeKind) -> String {
        switch kind {
        case .info: return "info.circle.fill"
        case .success: return "checkmark.circle.fill"
        case .warn: return "exclamationmark.triangle.fill"
        case .error: return "xmark.octagon.fill"
        }
    }

    private func tint(_ kind: NoticeKind) -> Color {
        switch kind {
        case .info: return SentinelTheme.accent
        case .success: return .green
        case .warn: return SentinelTheme.amber
        case .error: return SentinelTheme.alarm
        }
    }
}
