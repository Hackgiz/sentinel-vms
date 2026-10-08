import SwiftUI
import SentinelCore
import SentinelMediaServer

// Sentinel VMS is FREE — unlimited cameras, recording, live view, and remote
// access, no subscriptions. This sheet/page says so, offers an optional
// donation, and hosts the bring-your-own-key AI settings. The old per-camera and
// remote-access Stripe billing is gone (LicenseStore now reports unlimited).

struct UpgradeSheet: View {
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            ScrollView { FreeAndDonateContent().padding(20) }
            Divider()
            HStack {
                Text("Sentinel VMS is free — unlimited cameras, remote access, and recording.")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                Spacer()
                Button("Close") { dismiss() }
                    .buttonStyle(.bordered)
            }
            .padding(.horizontal, 20)
            .padding(.vertical, 14)
        }
        .frame(width: 520, height: 580)
        .background(SentinelTheme.background)
    }

    private var header: some View {
        VStack(spacing: 12) {
            Image(systemName: "checkmark.seal.fill")
                .font(.system(size: 48, weight: .light))
                .foregroundStyle(SentinelTheme.accent)
                .padding(.top, 32)

            Text("Everything's Free")
                .font(.title2.weight(.bold))

            Text("Unlimited cameras, recording, live view, and remote access — no subscriptions, no per-camera charges. If Sentinel is useful to you, an optional donation helps keep it going.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: 440)
                .padding(.horizontal, 24)
        }
        .padding(.bottom, 28)
    }
}

// MARK: - Plan page (Administration sidebar section)

struct PlanBillingView: View {
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                HStack(spacing: 10) {
                    Label("Sentinel VMS", systemImage: "checkmark.seal.fill")
                        .font(.headline)
                    Text("Free")
                        .font(.caption2.weight(.bold))
                        .padding(.horizontal, 7).padding(.vertical, 3)
                        .background(SentinelTheme.accent.opacity(0.14), in: Capsule())
                        .foregroundStyle(SentinelTheme.accent)
                    Spacer()
                }

                Text("Unlimited cameras, recording, live view, and remote access — all free, no subscriptions. AI scene descriptions are optional and run on your own Anthropic API key.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)

                FreeAndDonateContent()
            }
            .frame(maxWidth: 640, alignment: .leading)
            .padding(24)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(SentinelTheme.background)
    }
}

// MARK: - Shared content (free features + donate + AI key)

private struct FreeAndDonateContent: View {
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            FreeFeaturesCard()
            DonateCard()
            AIKeyCard()
        }
    }
}

private struct FreeFeaturesCard: View {
    private let features: [(icon: String, text: String)] = [
        ("video.fill", "Unlimited cameras — record, live view, playback"),
        ("globe", "Remote access from anywhere — no port-forwarding"),
        ("cpu", "On-device AI detection (person, vehicle, plate, animal…)"),
        ("iphone", "Free iPhone & iPad companion app"),
        ("lock.shield.fill", "Self-hosted & private — your Mac is the only server"),
    ]

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label("Included free, forever", systemImage: "gift.fill")
                .font(.callout.weight(.semibold))
            ForEach(features, id: \.icon) { f in
                HStack(spacing: 8) {
                    Image(systemName: f.icon)
                        .foregroundStyle(SentinelTheme.accent)
                        .frame(width: 18)
                    Text(f.text)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 10))
        .overlay { RoundedRectangle(cornerRadius: 10).stroke(SentinelTheme.line, lineWidth: 1) }
    }
}

// MARK: - Donate

struct DonateCard: View {
    @Environment(\.openURL) private var openURL

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label("Support Sentinel", systemImage: "heart.fill")
                .font(.callout.weight(.semibold))

            Text("Sentinel VMS is free and always will be. If it's useful to you, an optional donation funds new features and keeps development going — and your suggestions and feedback are just as welcome.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            if DonationConfig.isConfigured {
                Button {
                    openURL(DonationConfig.url)
                } label: {
                    Label("Donate", systemImage: "heart")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
            } else {
                Text("Donation link coming soon.")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 10))
        .overlay { RoundedRectangle(cornerRadius: 10).stroke(SentinelTheme.line, lineWidth: 1) }
    }
}
