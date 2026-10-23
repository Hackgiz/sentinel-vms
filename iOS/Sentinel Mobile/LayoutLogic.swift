// LayoutLogic.swift
// Width-aware layout without the views (WAVE18): how many live columns, when
// a list gets a detail pane beside it, how big tiles are. The iPhone Duo
// switches between compact (folded) and regular (unfolded) while the app
// runs, as an iPad does in Split View, so every decision keys off the
// horizontal size class and never off the device model. Kept pure so it is
// tested without a screen.

import CoreGraphics

/// The horizontal size class, reduced to the two cases the layouts care
/// about. `nil` (not yet known) counts as compact: the phone layout is the
/// safe one to show before the first real value arrives.
enum LayoutWidth: String, CaseIterable, Equatable {
    case compact
    case regular
}

enum LayoutLogic {
    // MARK: Live grid

    /// The live grid's column count before the user picks one.
    static func columns(for width: LayoutWidth) -> Int {
        width == .regular ? 3 : 2
    }

    /// Grid sizes offered in the menu. 4×4 only fits when unfolded or on iPad;
    /// on a folded phone its tiles are too small to read.
    static func columnChoices(for width: LayoutWidth) -> [Int] {
        width == .regular ? [1, 2, 3, 4] : [1, 2, 3]
    }

    /// The remembered choice for this width when it is still offered, else
    /// the default. 0 is what an unset `@AppStorage` Int reads as.
    static func liveColumns(stored: Int, for width: LayoutWidth) -> Int {
        columnChoices(for: width).contains(stored) ? stored : columns(for: width)
    }

    /// Each width remembers its own choice, so folding to the outer screen
    /// doesn't drag an unfolded 4×4 along (and back again).
    static func columnsStorageKey(for width: LayoutWidth) -> String {
        "live.columns.\(width.rawValue)"
    }

    static func gridSymbol(columns: Int) -> String {
        switch columns {
        case 1: return "square"
        case 2: return "square.grid.2x2"
        case 3: return "square.grid.3x3"
        default: return "square.grid.4x3.fill"
        }
    }

    static func gridLabel(columns: Int) -> String {
        "\(columns) × \(columns)"
    }

    // MARK: List and detail

    /// Events, Alarms and People show the selected item beside the list when
    /// there is room for both.
    static func usesSplit(for width: LayoutWidth) -> Bool {
        width == .regular
    }

    // MARK: Tiles

    /// Smallest width of a Home camera tile; the grid fits as many as it can.
    /// 150 pt gives two across on every phone; 220 pt gives three or four on
    /// the unfolded Duo instead of two stretched ones.
    static func tileMinimum(for width: LayoutWidth) -> CGFloat {
        width == .regular ? 220 : 150
    }

    /// The Home "Recent Activity" cards use the same width as a tile.
    static func stripCardSize(for width: LayoutWidth) -> CGSize {
        let w = tileMinimum(for: width)
        return CGSize(width: w, height: (w * 0.6).rounded())
    }

    // MARK: Video with controls

    /// Playback puts its controls beside the video instead of under it when
    /// the screen is wide and short: landscape unfolded or iPad landscape.
    /// A landscape phone is too narrow for both side by side.
    static func controlsBesideVideo(width: CGFloat, height: CGFloat) -> Bool {
        width >= 700 && width > height * 1.2
    }

    /// Width of the controls column when it sits beside the video.
    static let sideControlsWidth: CGFloat = 340

    // MARK: Navigation chrome (WAVE19)

    /// Regular width gets a persistent sidebar: the system TabView renders as
    /// a floating pill there (iOS 26+) that lands on top of the content.
    static func usesSidebar(for width: LayoutWidth) -> Bool {
        width == .regular
    }

    /// The sidebar rail: icon over label, like the Mac Operator rail.
    static let sidebarWidth: CGFloat = 84

    /// What the sidebar lists, top to bottom; Settings sits pinned at the
    /// bottom. People and Insights need /api/v2/me (or Demo Mode).
    static func sidebarTabs(showsPeopleAndInsights: Bool) -> [AppTab] {
        showsPeopleAndInsights ? [.home, .live, .events, .people, .insights] : [.home, .live, .events]
    }

    /// The tabs the compact TabView has. People and Insights are pushed
    /// from Home there instead.
    static let tabBarTabs: [AppTab] = [.home, .live, .events, .settings]

    /// Where navigation lands after the width changes, so a fold never shows
    /// a tab the new chrome doesn't have. Folding People or Insights opens it
    /// pushed on Home; unfolding a pushed one selects it in the sidebar.
    static func navigationAfterWidthChange(tab: AppTab, homePush: AppTab?,
                                           to width: LayoutWidth) -> (tab: AppTab, homePush: AppTab?) {
        switch width {
        case .compact:
            if tabBarTabs.contains(tab) { return (tab, homePush) }
            return (.home, tab)
        case .regular:
            if tab == .home, let push = homePush, push == .people || push == .insights { return (push, nil) }
            return (tab, nil)
        }
    }

    // MARK: Fullscreen video

    /// Whether the fullscreen cover turns the interface to landscape: only on
    /// an iPhone, where a portrait cover would show the same letterboxed band
    /// as the inline player. An iPad is already wide and keeps its orientation.
    static func fullscreenTurnsLandscape(phone: Bool) -> Bool { phone }

    /// Whether closing the cover turns the interface back to portrait: on an
    /// iPhone still held upright. A phone the user has turned sideways stays
    /// in landscape, as it would without the cover.
    static func fullscreenRestoresPortrait(phone: Bool, deviceIsLandscape: Bool) -> Bool {
        phone && deviceIsLandscape == false
    }

    // MARK: Side-mounted tab bar (wave19-fixes)

    /// On the iPhone Duo's outer screen iOS 27 stands the compact tab bar up
    /// as a vertical capsule at the bottom right. The SDK has no TabView
    /// placement or inset for that (`TabBarPlacement` only reports top,
    /// bottom, sidebar, ornament, page indicator), so the bar's frame is read
    /// from the window and scrolling content gets a trailing inset when the
    /// bar would come within `sideTabBarGap` of it.
    static let sideTabBarGap: CGFloat = 16

    /// A bar much taller than wide stands on the trailing edge; a bottom
    /// bar (402 × 83 on an iPhone 17 Pro) never qualifies.
    static func isSideMounted(tabBar frame: CGRect) -> Bool {
        frame.width > 0 && frame.height > frame.width * 2
    }

    /// Extra trailing inset so content ends at least `gap` before the bar.
    /// `containerWidth` and `safeTrailing` are the window's width and the
    /// trailing safe area content already respects.
    static func sideTabBarInset(tabBar frame: CGRect, containerWidth: CGFloat, safeTrailing: CGFloat,
                                width: LayoutWidth, gap: CGFloat = sideTabBarGap) -> CGFloat {
        guard width == .compact, isSideMounted(tabBar: frame) else { return 0 }
        let contentTrailing = containerWidth - safeTrailing
        return max(0, (contentTrailing - (frame.minX - gap)).rounded(.up))
    }

    /// The iPhone Duo's model identifier, and the trailing clearance its
    /// outer screen needs when the bar's frame can't be read.
    static let duoModelPrefix = "iPhone19,4"
    static let duoSideTabBarClearance: CGFloat = 84

    /// Fallback when the bar can't be measured: the Duo, compact, gets 84 pt
    /// of trailing clearance in all (what the system safe area already
    /// gives counts towards it). Every other device gets nothing.
    static func fallbackSideTabBarInset(model: String, width: LayoutWidth, safeTrailing: CGFloat) -> CGFloat {
        guard width == .compact, model.hasPrefix(duoModelPrefix) else { return 0 }
        return max(0, duoSideTabBarClearance - safeTrailing)
    }

    /// The measured inset when there is a bar frame, else the fallback.
    static func trailingTabBarInset(tabBar frame: CGRect?, containerWidth: CGFloat, safeTrailing: CGFloat,
                                    model: String, width: LayoutWidth) -> CGFloat {
        if let frame, frame.width > 0, frame.height > 0 {
            return sideTabBarInset(tabBar: frame, containerWidth: containerWidth, safeTrailing: safeTrailing, width: width)
        }
        return fallbackSideTabBarInset(model: model, width: width, safeTrailing: safeTrailing)
    }

    // MARK: Home dashboard (WAVE19)

    /// Home's content never stretches past this in landscape; it centres.
    static let dashboardMaxWidth: CGFloat = 1100

    /// The width Home's content gets from what is available, after padding.
    static func dashboardContentWidth(available: CGFloat, padding: CGFloat) -> CGFloat {
        max(0, min(available - padding * 2, dashboardMaxWidth))
    }

    /// The dashboard's two columns: 55 % "Needs attention" and activity,
    /// 45 % cameras and links, with `spacing` between them.
    static func dashboardColumns(contentWidth: CGFloat, spacing: CGFloat) -> (left: CGFloat, right: CGFloat) {
        let usable = max(0, contentWidth - spacing)
        let left = (usable * 0.55).rounded(.down)
        return (left, usable - left)
    }

    /// Home shows at most this many alarm cards under "Needs attention".
    static let dashboardAlarmLimit = 3
    /// The dashboard's camera preview grid is two across.
    static let dashboardPreviewColumns = 2

    // MARK: List and detail widths (WAVE19)

    /// The list column beside a detail pane (Events, People): 40 % of the
    /// width, held between 400 and 440 pt so cards never squeeze or sprawl.
    static func listColumnWidth(available: CGFloat) -> CGFloat {
        min(440, max(400, (available * 0.4).rounded()))
    }

    /// Regular-width alarm cards in a list show the action row only on the
    /// selected card; compact (and Home) always show it.
    static func alarmActionsVisible(width: LayoutWidth, inList: Bool, selected: Bool) -> Bool {
        guard width == .regular, inList else { return true }
        return selected
    }

    // MARK: Live focus layout (WAVE19)

    /// The live layouts. Focus (one big tile, the rest in a column) is a
    /// regular-width choice; folded, Live is always the grid.
    enum LiveLayout: String, CaseIterable, Equatable {
        case grid
        case focus
    }

    static let liveLayoutStorageKey = "live.layout.regular"

    static func liveLayout(stored: String?, for width: LayoutWidth) -> LiveLayout {
        guard width == .regular, let stored, let layout = LiveLayout(rawValue: stored) else { return .grid }
        return layout
    }

    /// The camera shown big, and the others in the side column in their own
    /// order. An unknown or missing focus falls back to the first camera.
    static func focusArrangement(cameraIDs: [String], focused: String?) -> (main: String?, others: [String]) {
        guard let main = focused.flatMap({ cameraIDs.contains($0) ? $0 : nil }) ?? cameraIDs.first else {
            return (nil, [])
        }
        return (main, cameraIDs.filter { $0 != main })
    }

    /// The side column in Focus: a quarter of the width, 220–320 pt.
    static func focusColumnWidth(available: CGFloat) -> CGFloat {
        min(320, max(220, (available * 0.25).rounded()))
    }
}

#if canImport(SwiftUI)
import SwiftUI

extension LayoutWidth {
    init(_ sizeClass: UserInterfaceSizeClass?) {
        self = sizeClass == .regular ? .regular : .compact
    }
}
#endif
