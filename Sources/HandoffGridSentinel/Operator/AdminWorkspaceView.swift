import SwiftUI
import SentinelCore

/// The rail's Admin workspace (wave 20 mac2): the Management tree in a
/// column next to the selected page, like the Duo's list-plus-detail, inside
/// the main window. The Management window (⇧⌘M) shows the same tree and
/// panes in a window of its own. Pages that aren't in the tree (the server
/// on this Mac, the 1.x importer, Settings) sit under it.
struct AdminWorkspaceView: View {
    @EnvironmentObject private var session: OperatorSession
    @EnvironmentObject private var navigator: OperatorNavigator
    @ObservedObject var selection: ManagementSelection
    let siteName: String
    let hasManagement: Bool

    static let treeWidth: CGFloat = 260

    /// Administration pages the tree doesn't cover for this role.
    private var localPages: [OperatorSection] {
        let model = ManagementNavModel(can: session.can)
        return OperatorSection.admin.filter { section in
            guard let node = AdministrationMenuItems.managementNode[section] else { return true }
            return model.node(node) == nil
        }
    }

    var body: some View {
        HStack(spacing: 0) {
            VStack(spacing: 0) {
                if hasManagement {
                    ManagementTree(selection: selection, siteName: siteName, embedded: true)
                } else {
                    Spacer()
                }
                Divider().overlay(SentinelTheme.line)
                VStack(alignment: .leading, spacing: 2) {
                    Text("This Mac").font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                        .padding(.horizontal, SentinelTheme.Space.sm)
                        .padding(.bottom, 2)
                    ForEach(localPages) { section in
                        Button { navigator.section = section } label: {
                            Label(section.rawValue, systemImage: section.symbol)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .padding(.horizontal, SentinelTheme.Space.sm)
                                .padding(.vertical, 4)
                                .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                    }
                }
                .padding(SentinelTheme.Space.sm)
            }
            .frame(width: Self.treeWidth)
            .background(SentinelTheme.chrome)
            Rectangle().fill(SentinelTheme.line).frame(width: 1)
            if hasManagement {
                ManagementDetail(selection: selection, siteName: siteName)
            } else {
                PermissionNote(text: "Your role can't open the site's management pages. The pages on the left are for this Mac.")
            }
        }
    }
}
