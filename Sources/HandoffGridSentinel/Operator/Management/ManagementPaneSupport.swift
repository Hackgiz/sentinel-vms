import SwiftUI
import SentinelCore

/// Shared bits for the wave-6 management panes. Every pane takes the site's
/// `SentinelAPI` directly (no environment objects) so it runs standalone
/// behind a `#Preview` and the Management window can host it unchanged.

func managementErrorText(_ error: Error) -> String {
    (error as? LocalizedError)?.errorDescription ?? error.localizedDescription
}

enum ManagementPreview {
    /// An API pointed at nothing: previews show each pane's error/empty state.
    static func api() -> SentinelAPI {
        SentinelAPI(baseURL: URL(string: "http://127.0.0.1:9")!, siteID: "preview",
                    tokenStore: MemoryTokenStore(), clientLabel: "Preview")
    }
}

/// Title row + optional error/status lines above a pane's content.
struct PaneFrame<Content: View, Trailing: View>: View {
    var title: String
    var subtitle: String?
    var error: String?
    var status: String?
    @ViewBuilder var trailing: () -> Trailing
    @ViewBuilder var content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .firstTextBaseline) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(title).font(.title3.weight(.semibold))
                    if let subtitle { Text(subtitle).font(.caption).foregroundStyle(.secondary) }
                }
                Spacer()
                trailing()
            }
            .padding(14)
            Divider()
            if let error {
                Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red)
                    .fixedSize(horizontal: false, vertical: true).padding(.horizontal, 14).padding(.top, 8)
            }
            if let status {
                Text(status).font(.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true).padding(.horizontal, 14).padding(.top, 8)
            }
            content()
        }
    }
}

extension PaneFrame where Trailing == EmptyView {
    init(title: String, subtitle: String? = nil, error: String? = nil, status: String? = nil,
         @ViewBuilder content: @escaping () -> Content) {
        self.init(title: title, subtitle: subtitle, error: error, status: status, trailing: { EmptyView() }, content: content)
    }
}

/// Runs `body` every `seconds` while `active` (one visible-pane interval).
/// Use as `.task(id: active) { await visibleInterval(active, seconds: 3) { ... } }`.
func visibleInterval(_ active: Bool, seconds: UInt64, _ body: @MainActor () async -> Void) async {
    guard active else { return }
    while Task.isCancelled == false {
        try? await Task.sleep(nanoseconds: seconds * 1_000_000_000)
        if Task.isCancelled { return }
        await body()
    }
}

/// Tracks whether a pane is on screen, for its interval.
struct OnScreenModifier: ViewModifier {
    @Binding var onScreen: Bool
    func body(content: Content) -> some View {
        content.onAppear { onScreen = true }.onDisappear { onScreen = false }
    }
}

extension View {
    func trackOnScreen(_ binding: Binding<Bool>) -> some View { modifier(OnScreenModifier(onScreen: binding)) }
}
