import AppKit
import SwiftUI
import SentinelCore

// Wave 23 phone cameras on the Mac: the small phone badge, the "Use an
// iPhone" sheet behind Add Camera, and the push-key section of the camera
// editor. Wording and rules are in SentinelCore's PhoneCameraLogic.

/// The small phone glyph on live tiles and in the cameras list.
struct PhoneCameraBadge: View {
    var onVideo = true

    var body: some View {
        Image(systemName: PhoneCameraLogic.badgeSymbol)
            .font(.system(size: 9, weight: .semibold))
            .foregroundStyle(onVideo ? Color.white : Color.secondary)
            .padding(.horizontal, onVideo ? 5 : 0).padding(.vertical, onVideo ? 3 : 0)
            .background(onVideo ? Color.black.opacity(0.55) : Color.clear, in: Capsule())
            .help(PhoneCameraLogic.badgeHelp)
            .accessibilityLabel(PhoneCameraLogic.badgeHelp)
    }
}

/// "Uploaded from iPhone" on evidence items sent from the phone.
struct UploadedFromIPhoneTag: View {
    var body: some View {
        Label(EvidenceDisplay.uploadedBadge, systemImage: "iphone")
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 5).padding(.vertical, 1)
            .background(SentinelTheme.accent.opacity(0.18), in: Capsule())
            .foregroundStyle(SentinelTheme.accent)
    }
}

/// Addendum 2: "Needs Sentinel Cloud." with a Learn more link to sentvms.com/cloud.
struct NeedsCloudNote: View {
    var body: some View {
        HStack(spacing: 6) {
            Label(PhoneCameraLogic.cloudNote, systemImage: "cloud").font(.caption.weight(.semibold))
            Link(PhoneCameraLogic.learnMoreTitle, destination: PhoneCameraLogic.learnMoreURL).font(.caption)
        }
    }
}

/// Add Camera › Use an iPhone: phone cameras are created from the iPhone itself.
struct UseIPhoneSheet: View {
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 10) {
                Image(systemName: "iphone").font(.system(size: 26)).foregroundStyle(SentinelTheme.accent)
                VStack(alignment: .leading, spacing: 2) {
                    Text(PhoneCameraLogic.useIPhoneTitle).font(.headline)
                    Text(PhoneCameraLogic.useIPhoneSummary).font(.callout).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            VStack(alignment: .leading, spacing: 8) {
                ForEach(Array(PhoneCameraLogic.useIPhoneSteps.enumerated()), id: \.offset) { index, step in
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text("\(index + 1).").font(.callout.weight(.semibold)).frame(width: 18, alignment: .trailing)
                        Text(step).font(.callout).fixedSize(horizontal: false, vertical: true)
                    }
                }
            }
            Text(PhoneCameraLogic.useIPhoneFootnote).font(.caption).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            NeedsCloudNote() // addendum 2
            HStack {
                Spacer()
                Button("Done") { dismiss() }.keyboardShortcut(.defaultAction)
            }
        }
        .padding(20)
        .frame(width: 440)
    }
}

/// The camera editor's section for a phone camera: status, Rotate key, Revoke key.
struct PhoneCameraKeySection: View {
    @EnvironmentObject private var runtime: SiteRuntime
    let camera: V2.Camera

    @State private var confirmRotate = false
    @State private var confirmRevoke = false
    @State private var busy = false
    @State private var newKey: String?
    @State private var status: String?
    @State private var error: String?

    var body: some View {
        Section("iPhone camera") {
            HStack(spacing: 6) {
                PhoneCameraBadge(onVideo: false)
                Text(PhoneCameraLogic.summary(camera))
                    .foregroundStyle(PhoneCameraLogic.needsCloud(camera) ? Color.orange : camera.status.online ? Color.green : Color.secondary)
            }
            if PhoneCameraLogic.needsCloud(camera) { NeedsCloudNote() } // addendum 2
            if camera.status.online, camera.status.message.isEmpty == false, PhoneCameraLogic.needsCloud(camera) == false {
                Text(camera.status.message).font(.caption).foregroundStyle(.secondary)
            }
            Text("No RTSP or ONVIF settings: the iPhone sends its video to Sentinel over HTTPS.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            HStack {
                Button("Rotate Key…") { confirmRotate = true }
                Button("Revoke Key…", role: .destructive) { confirmRevoke = true }
                if busy { ProgressView().controlSize(.small) }
            }
            .disabled(busy)
            if let newKey {
                VStack(alignment: .leading, spacing: 4) {
                    HStack {
                        Text(newKey).font(.system(size: 11, design: .monospaced)).textSelection(.enabled).lineLimit(1)
                            .truncationMode(.middle)
                        Button("Copy") {
                            NSPasteboard.general.clearContents()
                            NSPasteboard.general.setString(newKey, forType: .string)
                        }
                        .controlSize(.small)
                    }
                    Text(PhoneCameraLogic.newKeyNote).font(.caption).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            if let status { Text(status).font(.caption).foregroundStyle(.secondary) }
            if let error {
                Text(error).font(.caption.weight(.semibold)).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
            }
        }
        .confirmationDialog(PhoneCameraLogic.rotateTitle, isPresented: $confirmRotate) {
            Button("Rotate Key", role: .destructive) { Task { await rotate() } }
        } message: {
            Text(PhoneCameraLogic.rotateMessage)
        }
        .confirmationDialog(PhoneCameraLogic.revokeTitle, isPresented: $confirmRevoke) {
            Button("Revoke Key", role: .destructive) { Task { await revoke() } }
        } message: {
            Text(PhoneCameraLogic.revokeMessage)
        }
    }

    private func rotate() async {
        busy = true
        defer { busy = false }
        error = nil
        status = nil
        do {
            let answer = try await runtime.api.rotatePushKey(cameraID: camera.id)
            newKey = answer.pushKey.isEmpty ? nil : answer.pushKey
            status = "New key made. The old one no longer works."
        } catch {
            self.error = error.localizedDescription
        }
    }

    private func revoke() async {
        busy = true
        defer { busy = false }
        error = nil
        status = nil
        do {
            try await runtime.api.revokePushKey(cameraID: camera.id)
            newKey = nil
            status = "Key revoked. The iPhone can't stream until you rotate in a new key."
        } catch {
            self.error = error.localizedDescription
        }
    }
}
