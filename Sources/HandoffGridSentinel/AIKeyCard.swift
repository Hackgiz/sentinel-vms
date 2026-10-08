import SwiftUI
import SentinelCore

// MARK: - Bring-your-own-key AI settings card
//
// Sentinel's AI features (scene descriptions, daily digests, event search) run
// on the operator's OWN Anthropic API key — Sentinel calls Claude directly and
// never sees the key or the usage/bill. This card lets the operator paste a
// key, verify it against Anthropic, store it in the encrypted vault, and toggle
// AI on. Shown in the AI tab and the upgrade sheet.
struct AIKeyCard: View {
    enum CheckState { case idle, checking, valid, invalid, saved }

    @State private var keyInput = ""
    @State private var enabled = SentinelAISettings.isEnabled
    @State private var hasStoredKey = SentinelAISettings.hasAPIKey
    @State private var state: CheckState = .idle

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Label("AI — Claude (your key)", systemImage: "sparkles")
                    .font(.callout.weight(.semibold))
                Spacer()
                if SentinelAISettings.isAvailable {
                    Text("ON")
                        .font(.caption2.weight(.bold))
                        .padding(.horizontal, 7).padding(.vertical, 3)
                        .background(Color.green.opacity(0.16), in: Capsule())
                        .foregroundStyle(.green)
                }
            }

            Text("Bring your own Anthropic API key. Sentinel runs scene descriptions, daily digests, and natural-language event search directly against Claude on your account — your key never leaves this Mac and Sentinel never sees the bill. Typically pennies per alert.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            HStack(spacing: 8) {
                SecureField(hasStoredKey ? "Key saved — paste a new one to replace" : "sk-ant-…",
                            text: $keyInput)
                    .textFieldStyle(.roundedBorder)
                    .onSubmit(save)
                Button("Save", action: save)
                    .disabled(keyInput.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || state == .checking)
            }

            Toggle(isOn: $enabled) {
                Text("Enable AI features")
                    .font(.callout)
            }
            .toggleStyle(.checkbox)
            .disabled(!hasStoredKey)
            .onChange(of: enabled) { newValue in
                SentinelAISettings.isEnabled = newValue
            }

            statusLine

            HStack(spacing: 12) {
                Link("Get a key at console.anthropic.com →",
                     destination: URL(string: "https://console.anthropic.com/settings/keys")!)
                    .font(.caption2)
                if hasStoredKey {
                    Spacer()
                    Button("Remove key", role: .destructive, action: remove)
                        .controlSize(.small)
                }
            }
        }
        .padding(16)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 10))
        .overlay { RoundedRectangle(cornerRadius: 10).stroke(SentinelTheme.line, lineWidth: 1) }
    }

    @ViewBuilder private var statusLine: some View {
        switch state {
        case .checking:
            HStack(spacing: 6) {
                ProgressView().controlSize(.small)
                Text("Verifying key with Anthropic…").font(.caption2).foregroundStyle(.secondary)
            }
        case .valid:
            Label("Key verified with Anthropic.", systemImage: "checkmark.seal.fill")
                .font(.caption2).foregroundStyle(.green)
        case .invalid:
            Label("Anthropic rejected this key — saved anyway, but check it.", systemImage: "exclamationmark.triangle.fill")
                .font(.caption2).foregroundStyle(.orange)
        case .saved:
            Label("Key saved.", systemImage: "checkmark").font(.caption2).foregroundStyle(.secondary)
        case .idle:
            if hasStoredKey && !enabled {
                Text("Key saved. Turn on “Enable AI features” to start.")
                    .font(.caption2).foregroundStyle(.secondary)
            }
        }
    }

    // MARK: Actions

    private func save() {
        let key = keyInput.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !key.isEmpty else { return }
        // Store immediately (a transient network blip shouldn't block saving),
        // then verify in the background to set the status indicator.
        SentinelAISettings.setAPIKey(key)
        hasStoredKey = true
        keyInput = ""
        if !enabled { enabled = true; SentinelAISettings.isEnabled = true }
        state = .checking
        Task {
            let ok = await SentinelAIClient.validate(key: key)
            await MainActor.run { state = ok ? .valid : .invalid }
        }
    }

    private func remove() {
        SentinelAISettings.setAPIKey(nil)
        hasStoredKey = false
        enabled = false
        SentinelAISettings.isEnabled = false
        keyInput = ""
        state = .idle
    }
}
