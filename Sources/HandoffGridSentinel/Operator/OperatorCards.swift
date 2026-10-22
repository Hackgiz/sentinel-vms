import AppKit
import SwiftUI
import SentinelCore

// Wave 20: the shared pieces of the Duo look (the iPhone's unfolded
// layout): page header, card, chips, stat tile, the alarm card. Home, the
// Alarm Manager, the Alarm Queue and Search build from these so a list of
// alarms or events looks the same wherever it shows.

/// A workspace's title: the large weight, one line under it with the count
/// or state, and the workspace's controls on the right.
struct PageHeader<Leading: View, Trailing: View>: View {
    let title: String
    var subtitle: String?
    /// A dot before the subtitle (the site's connection on Home and Live).
    var dot: Color?
    @ViewBuilder var leading: () -> Leading
    @ViewBuilder var trailing: () -> Trailing

    var body: some View {
        HStack(alignment: .center, spacing: SentinelTheme.Space.md) {
            leading()
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.system(size: 24, weight: .bold))
                    .lineLimit(1)
                if let subtitle {
                    HStack(spacing: 6) {
                        if let dot { Circle().fill(dot).frame(width: 7, height: 7) }
                        Text(subtitle)
                            .font(.callout.weight(.medium))
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                }
            }
            .layoutPriority(1)
            Spacer(minLength: SentinelTheme.Space.md)
            trailing()
        }
    }
}

extension PageHeader where Leading == EmptyView {
    init(title: String, subtitle: String? = nil, dot: Color? = nil, @ViewBuilder trailing: @escaping () -> Trailing) {
        self.init(title: title, subtitle: subtitle, dot: dot, leading: { EmptyView() }, trailing: trailing)
    }
}

/// A section's title inside a page, with a link on the right ("All alarms").
struct SectionTitle: View {
    let title: String
    var actionLabel: String?
    var action: (() -> Void)?

    var body: some View {
        HStack(alignment: .firstTextBaseline) {
            Text(title).font(.title3.weight(.semibold))
            Spacer()
            if let actionLabel, let action {
                Button(actionLabel, action: action)
                    .buttonStyle(.plain)
                    .font(.callout.weight(.semibold))
                    .foregroundStyle(SentinelTheme.accent)
            }
        }
    }
}

/// The card surface: `panel`, a 1 pt `line` border, the medium radius; an
/// optional 3 pt stripe on the left (alarm severity) and an accent border
/// when selected.
struct OperatorCardStyle: ViewModifier {
    var stripe: Color?
    var selected = false
    var border: Color?
    var radius: CGFloat = SentinelTheme.Radius.md

    func body(content: Content) -> some View {
        content
            .padding(.leading, stripe == nil ? 0 : 3)
            .background(SentinelTheme.panel)
            .overlay(alignment: .leading) {
                if let stripe { Rectangle().fill(stripe).frame(width: 3) }
            }
            .clipShape(RoundedRectangle(cornerRadius: radius, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: radius, style: .continuous)
                    .stroke(selected ? SentinelTheme.accent : (border ?? SentinelTheme.line), lineWidth: selected ? 2 : 1)
            )
    }
}

extension View {
    func operatorCard(stripe: Color? = nil, selected: Bool = false, border: Color? = nil,
                      radius: CGFloat = SentinelTheme.Radius.md) -> some View {
        modifier(OperatorCardStyle(stripe: stripe, selected: selected, border: border, radius: radius))
    }
}

extension HomeModel.Tone {
    var color: Color {
        switch self {
        case .good: return SentinelTheme.recording
        case .warning: return SentinelTheme.amber
        case .bad: return SentinelTheme.alarm
        case .info: return SentinelTheme.accent
        }
    }
}

/// A soft pill: a tinted capsule with coloured text.
struct ToneChip: View {
    let text: String
    var systemImage: String?
    var color: Color = SentinelTheme.accent
    var uppercase = false

    var body: some View {
        HStack(spacing: 4) {
            if let systemImage { Image(systemName: systemImage).font(.caption2.weight(.bold)) }
            Text(uppercase ? text.uppercased() : text)
                .font(.caption2.weight(uppercase ? .heavy : .bold))
                .lineLimit(1)
        }
        .foregroundStyle(color)
        .padding(.horizontal, 8)
        .padding(.vertical, 3)
        .background(color.opacity(0.16), in: Capsule())
        .fixedSize()
    }
}

/// A detection kind (Person, Vehicle, License Plate, Loitering, Watchlist…)
/// in the iPhone's chip colours.
struct KindChip: View {
    let kind: String
    var filled = false

    var body: some View {
        let color = SentinelTheme.kindColor(kind)
        HStack(spacing: 4) {
            Image(systemName: HomeModel.kindSymbol(kind)).font(.caption2.weight(.bold))
            Text(kind.uppercased()).font(.caption2.weight(.heavy))
        }
        .foregroundStyle(filled ? .white : color)
        .padding(.horizontal, 8)
        .padding(.vertical, 3)
        .background(filled ? color : color.opacity(0.16), in: Capsule())
        .fixedSize()
    }
}

/// The square icon tile at the start of a card.
struct IconTile: View {
    let symbol: String
    let color: Color
    var size: CGFloat = 36

    var body: some View {
        RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous)
            .fill(color.opacity(0.16))
            .frame(width: size, height: size)
            .overlay(Image(systemName: symbol).font(.system(size: size * 0.42, weight: .bold)).foregroundStyle(color))
    }
}

/// One number on Home's top row: what it counts, the number, a toned note.
struct StatTile: View {
    let stat: HomeModel.Stat
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
                HStack(spacing: 6) {
                    Image(systemName: stat.symbol)
                        .font(.caption.weight(.bold))
                        .foregroundStyle(stat.tone.color)
                    Text(stat.title.uppercased())
                        .font(.caption2.weight(.bold))
                        .tracking(0.6)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
                Text(stat.value)
                    .font(.system(size: 30, weight: .bold, design: .rounded))
                    .monospacedDigit()
                    .lineLimit(1)
                    .minimumScaleFactor(0.6)
                ToneChip(text: stat.note, color: stat.tone.color)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(SentinelTheme.Space.lg)
            .operatorCard()
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(stat.title): \(stat.value), \(stat.note)")
        .accessibilityAddTraits(.isButton)
    }
}

/// An alarm as a card: severity stripe, kind icon tile, title and severity
/// on one line, camera · time beneath, one line of detail, and the action
/// row only while the card is selected or hovered.
struct AlarmCard<Actions: View>: View {
    let alarm: V2.Alarm
    var cameraName: String?
    var selected = false
    /// Home keeps the action row showing; lists show it on hover or selection.
    var alwaysShowActions = false
    @ViewBuilder var actions: () -> Actions

    @State private var hovered = false

    private var color: Color { SentinelTheme.alarmColor(kind: alarm.kind, severity: alarm.severity) }

    var body: some View {
        VStack(alignment: .leading, spacing: SentinelTheme.Space.sm) {
            HStack(alignment: .center, spacing: SentinelTheme.Space.md) {
                IconTile(symbol: HomeModel.alarmSymbol(kind: alarm.kind, severity: alarm.severity), color: color)
                VStack(alignment: .leading, spacing: 3) {
                    HStack(alignment: .firstTextBaseline, spacing: SentinelTheme.Space.sm) {
                        Text(alarm.title)
                            .font(.headline)
                            .lineLimit(1)
                        Spacer(minLength: 4)
                        ToneChip(text: alarm.severity.rawValue, color: SentinelTheme.severityColor(alarm.severity), uppercase: true)
                    }
                    HStack(spacing: 4) {
                        if let name = cameraName ?? alarm.cameraName, name.isEmpty == false {
                            Text(name)
                            Text("·")
                        }
                        TimelineView(.periodic(from: .now, by: 30)) { context in
                            Text(HomeModel.relative(alarm.lastEventAt, now: context.date))
                        }
                        if alarm.eventCount > 1 {
                            Text("· \(alarm.eventCount) events")
                        }
                        if alarm.owner.isEmpty == false {
                            Text("· \(alarm.owner)")
                        }
                        Spacer(minLength: 4)
                        Text(alarm.state.rawValue)
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(SentinelTheme.stateColor(alarm.state))
                    }
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                }
            }
            if alarm.detail.isEmpty == false {
                Text(alarm.detail)
                    .font(.callout)
                    .foregroundStyle(.primary.opacity(0.8))
                    .lineLimit(1)
            }
            if alwaysShowActions || selected || hovered {
                HStack(spacing: SentinelTheme.Space.sm) { actions() }
                    .controlSize(.regular)
            }
        }
        .padding(SentinelTheme.Space.md)
        .frame(maxWidth: .infinity, alignment: .leading)
        .operatorCard(stripe: color, selected: selected,
                      border: alarm.state == .new ? color.opacity(0.45) : nil)
        .contentShape(Rectangle())
        .onHover { hovered = $0 }
        .animation(.easeInOut(duration: 0.12), value: hovered)
    }
}

/// The big blue action in a card's action row (Acknowledge).
struct CardPrimaryButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.callout.weight(.semibold))
            .foregroundStyle(.white)
            .frame(maxWidth: .infinity)
            .frame(height: 30)
            .background(SentinelTheme.accent.opacity(configuration.isPressed ? 0.75 : 1),
                        in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
            .contentShape(Rectangle())
    }
}

/// A square secondary action in a card's action row (pin, camera, play).
struct CardIconButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.callout.weight(.semibold))
            .frame(minWidth: 40)
            .frame(height: 30)
            .background(SentinelTheme.panelRaised.opacity(configuration.isPressed ? 0.6 : 1),
                        in: RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: SentinelTheme.Radius.sm, style: .continuous).stroke(SentinelTheme.line, lineWidth: 1))
            .contentShape(Rectangle())
    }
}

/// A camera's tile on Home: its latest snapshot (or its symbol), REC and
/// Online badges, the name and location.
struct CameraStillTile: View {
    @EnvironmentObject private var runtime: SiteRuntime
    let camera: V2.Camera
    let still: V2.Event?
    @State private var image: NSImage?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Color.clear
                .aspectRatio(16 / 9, contentMode: .fit)
                .overlay { preview }
                .clipped()
                .overlay(alignment: .top) {
                    HStack {
                        if camera.recording && camera.status.online { recPill }
                        Spacer()
                        // A dark backing keeps the badge legible over a bright still.
                        ToneChip(text: camera.status.online ? "Online" : "Offline",
                                 color: camera.status.online ? SentinelTheme.recording : SentinelTheme.alarm)
                            .background(.black.opacity(0.6), in: Capsule())
                    }
                    .padding(SentinelTheme.Space.sm)
                }
            VStack(alignment: .leading, spacing: 2) {
                Text(camera.name).font(.callout.weight(.semibold)).lineLimit(1)
                HStack(spacing: 5) {
                    Image(systemName: "mappin.circle.fill").font(.caption2)
                    Text(camera.location.isEmpty ? camera.resolutionLabel : camera.location).font(.caption).lineLimit(1)
                }
                .foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 10)
            .padding(.vertical, SentinelTheme.Space.sm)
            .background(SentinelTheme.chrome)
        }
        .operatorCard(radius: SentinelTheme.Radius.sm)
        .task(id: still?.id) {
            guard let still else { image = nil; return }
            image = await runtime.thumbnails.image(for: still.id)
        }
    }

    @ViewBuilder
    private var preview: some View {
        ZStack {
            LinearGradient(colors: [Color(white: 0.14), Color(white: 0.05)], startPoint: .topLeading, endPoint: .bottomTrailing)
            if let image {
                Image(nsImage: image).resizable().aspectRatio(contentMode: .fill)
            } else {
                Image(systemName: camera.status.online ? "video.fill" : "video.slash.fill")
                    .font(.title2)
                    .foregroundStyle(camera.status.online ? Color.white.opacity(0.3) : SentinelTheme.alarm.opacity(0.7))
            }
        }
    }

    private var recPill: some View {
        HStack(spacing: 4) {
            Circle().fill(SentinelTheme.alarm).frame(width: 6, height: 6)
            Text("REC").font(.caption2.weight(.heavy)).foregroundStyle(.white)
        }
        .padding(.horizontal, 6)
        .padding(.vertical, 3)
        .background(.black.opacity(0.55), in: Capsule())
    }
}

/// An event's snapshot from the shared thumbnail cache, or a kind-tinted
/// placeholder when the server saved no frame.
struct EventThumb: View {
    @EnvironmentObject private var runtime: SiteRuntime
    let event: V2.Event
    var contentMode: ContentMode = .fill
    @State private var image: NSImage?

    var body: some View {
        ZStack {
            LinearGradient(colors: [Color(white: 0.14), Color(white: 0.05)], startPoint: .topLeading, endPoint: .bottomTrailing)
            if let image {
                Image(nsImage: image).resizable().aspectRatio(contentMode: contentMode)
            } else {
                Image(systemName: HomeModel.kindSymbol(event.kind))
                    .font(.title3)
                    .foregroundStyle(SentinelTheme.kindColor(event.kind).opacity(0.8))
            }
        }
        .clipped()
        .task(id: event.id) {
            guard event.hasThumbnail else { return }
            image = await runtime.thumbnails.image(for: event.id)
        }
    }
}
