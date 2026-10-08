import AppKit
import SwiftUI
import SentinelCore

private struct SentinelShield: InsettableShape {
    var insetAmount: CGFloat = 0

    func path(in rect: CGRect) -> Path {
        let r = rect.insetBy(dx: insetAmount, dy: insetAmount)
        let cr = r.width * 0.16
        var p = Path()

        p.move(to: CGPoint(x: r.minX + cr, y: r.minY))
        p.addLine(to: CGPoint(x: r.maxX - cr, y: r.minY))
        p.addArc(
            center: CGPoint(x: r.maxX - cr, y: r.minY + cr),
            radius: cr,
            startAngle: .degrees(-90), endAngle: .degrees(0),
            clockwise: false
        )
        p.addCurve(
            to: CGPoint(x: r.midX, y: r.maxY),
            control1: CGPoint(x: r.maxX, y: r.midY + r.height * 0.06),
            control2: CGPoint(x: r.midX + r.width * 0.27, y: r.maxY - r.height * 0.07)
        )
        p.addCurve(
            to: CGPoint(x: r.minX, y: r.minY + cr),
            control1: CGPoint(x: r.midX - r.width * 0.27, y: r.maxY - r.height * 0.07),
            control2: CGPoint(x: r.minX, y: r.midY + r.height * 0.06)
        )
        p.addArc(
            center: CGPoint(x: r.minX + cr, y: r.minY + cr),
            radius: cr,
            startAngle: .degrees(180), endAngle: .degrees(-90),
            clockwise: false
        )
        p.closeSubpath()
        return p
    }

    func inset(by amount: CGFloat) -> SentinelShield {
        var s = self; s.insetAmount += amount; return s
    }
}

struct HandoffGridMark: View {
    let size: CGFloat

    var body: some View {
        let borderWidth = max(1.5, size * 0.065)

        ZStack {
            SentinelShield()
                .fill(
                    LinearGradient(
                        colors: [
                            SentinelTheme.accent.opacity(0.24),
                            Color.white.opacity(0.05)
                        ],
                        startPoint: .topLeading,
                        endPoint: .bottomTrailing
                    )
                )

            SentinelShield()
                .strokeBorder(SentinelTheme.accent, lineWidth: borderWidth)

            VStack(spacing: size * 0.08) {
                HStack(spacing: size * 0.08) {
                    gridCell(opacity: 0.95)
                    gridCell(opacity: 0.38)
                }
                HStack(spacing: size * 0.08) {
                    gridCell(opacity: 0.38)
                    gridCell(opacity: 0.95)
                }
            }
            .padding(.top, size * 0.17)
            .padding(.horizontal, size * 0.19)
            .padding(.bottom, size * 0.33)
        }
        .frame(width: size, height: size)
        .accessibilityLabel("Sentinel VMS logo")
    }

    private func gridCell(opacity: Double) -> some View {
        RoundedRectangle(cornerRadius: size * 0.05)
            .fill(.white.opacity(opacity))
    }
}

struct HandoffGridLogo: View {
    let markSize: CGFloat

    init(markSize: CGFloat = 32) {
        self.markSize = markSize
    }

    var body: some View {
        HStack(spacing: max(8, markSize * 0.28)) {
            HandoffGridMark(size: markSize)

            VStack(alignment: .leading, spacing: 2) {
                Text("SENTINEL VMS")
                    .font(.system(size: max(12, markSize * 0.34), weight: .bold, design: .rounded))
                    .foregroundStyle(.primary)

                Text("by HandoffGrid")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
        }
        .accessibilityElement(children: .combine)
    }
}

struct SentinelPanel<Content: View>: View {
    let title: String
    let systemImage: String?
    let content: Content

    init(_ title: String, systemImage: String? = nil, @ViewBuilder content: () -> Content) {
        self.title = title
        self.systemImage = systemImage
        self.content = content()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                if let systemImage {
                    Image(systemName: systemImage)
                        .foregroundStyle(SentinelTheme.accent)
                }

                Text(title)
                    .font(.caption.weight(.bold))
                    .kerning(0.8)
                    .foregroundStyle(.primary)

                Spacer()
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 10)
            .background(SentinelTheme.chrome)

            Divider()
                .overlay(SentinelTheme.line)

            content
                .padding(14)
        }
        .background(SentinelTheme.panel)
        .clipShape(RoundedRectangle(cornerRadius: 8))
        .overlay {
            RoundedRectangle(cornerRadius: 8)
                .stroke(SentinelTheme.line, lineWidth: 1)
        }
    }
}

struct StatusBadge: View {
    let status: CameraStatus

    var body: some View {
        Label(status.label, systemImage: status.symbol)
            .font(.caption2.weight(.bold))
            .kerning(0.5)
            .padding(.horizontal, 8)
            .padding(.vertical, 5)
            .foregroundStyle(status.tint)
            .background(status.tint.opacity(0.16), in: RoundedRectangle(cornerRadius: 4))
    }
}

struct SeverityBadge: View {
    let severity: AlertSeverity

    var body: some View {
        Text(severity.rawValue)
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 8)
            .padding(.vertical, 5)
            .foregroundStyle(severity.tint)
            .background(severity.tint.opacity(0.16), in: Capsule())
    }
}

struct MetricPill: View {
    let text: String

    var body: some View {
        Text(text)
            .font(.caption2.weight(.medium))
            .foregroundStyle(.primary)
            .lineLimit(1)
            .minimumScaleFactor(0.78)
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .background(.primary.opacity(0.10), in: Capsule())
    }
}

struct RecordingPill: View {
    var body: some View {
        HStack(spacing: 5) {
            Circle()
                .fill(.red)
                .frame(width: 6, height: 6)

            Text("REC")
                .font(.caption2.weight(.bold))
        }
        .foregroundStyle(.white)
        .padding(.horizontal, 8)
        .padding(.vertical, 4)
        .background(.red.opacity(0.68), in: Capsule())
    }
}

struct DetailRow: View {
    let label: String
    let value: String

    var body: some View {
        HStack {
            Text(label)
                .font(.caption)
                .foregroundStyle(.secondary)

            Spacer()

            Text(value)
                .font(.caption.weight(.semibold))
                .lineLimit(1)
        }
    }
}

struct MeterBar: View {
    let value: Double  // 0.0–1.0
    let tint: Color

    var body: some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: 2)
                    .fill(Color.primary.opacity(0.07))

                RoundedRectangle(cornerRadius: 2)
                    .fill(
                        LinearGradient(
                            colors: [tint.opacity(0.75), tint],
                            startPoint: .leading,
                            endPoint: .trailing
                        )
                    )
                    .frame(width: max(4, CGFloat(min(value, 1.0)) * geo.size.width))
            }
        }
        .frame(height: 4)
    }
}

struct MetricCard: View {
    let title: String
    let value: String
    let detail: String
    let tint: Color

    var body: some View {
        HStack(spacing: 0) {
            // Left accent bar
            Rectangle()
                .fill(tint)
                .frame(width: 3)

            VStack(alignment: .leading, spacing: 4) {
                Text(value)
                    .font(.system(size: 20, weight: .bold, design: .monospaced))
                    .foregroundStyle(tint)
                    .lineLimit(1)
                    .minimumScaleFactor(0.6)

                Text(title)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
                    .lineLimit(1)

                Text(detail)
                    .font(.caption2)
                    .foregroundStyle(.tertiary)
                    .lineLimit(1)
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .background(SentinelTheme.panelRaised)
        .clipShape(RoundedRectangle(cornerRadius: 6))
        .overlay {
            RoundedRectangle(cornerRadius: 6)
                .stroke(tint.opacity(0.18), lineWidth: 1)
        }
    }
}

struct EmptyStateLine: View {
    let text: String

    var body: some View {
        Text(text)
            .font(.caption)
            .foregroundStyle(.secondary)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(12)
            .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
    }
}

struct PulsingAlertRing: View {
    var cornerRadius: CGFloat = 8
    var color: Color = .red
    @State private var pulsing = false

    var body: some View {
        RoundedRectangle(cornerRadius: cornerRadius)
            .stroke(color, lineWidth: 2.5)
            .opacity(pulsing ? 0.22 : 0.92)
            .onAppear {
                withAnimation(.easeInOut(duration: 1.1).repeatForever(autoreverses: true)) {
                    pulsing = true
                }
            }
    }
}
