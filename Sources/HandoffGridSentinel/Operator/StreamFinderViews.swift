import SwiftUI
import SentinelCore

// Wave 22 addendum on the Mac: the camera editor's "Second stream (low-res)"
// field and its Find streams picker. Rules and wording live in SentinelCore's
// StreamFinderLogic.

/// The field, its one-line explanation and the Find streams button.
struct SecondStreamField: View {
    @Binding var url: String
    /// Saved camera and a server that knows the discover endpoint.
    let canFind: Bool
    let isSaved: Bool
    let serverSupportsDiscover: Bool
    let find: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline) {
                TextField(StreamFinderLogic.fieldLabel, text: $url, prompt: Text("rtsp://192.168.1.50:554/stream2"))
                if canFind {
                    Button {
                        find()
                    } label: {
                        Label(StreamFinderLogic.findButton, systemImage: "magnifyingglass")
                    }
                    .help("Ask the camera which streams it has")
                }
            }
            Text(StreamFinderLogic.fieldHelp).font(.caption).foregroundStyle(.secondary)
            if isSaved == false {
                Text(StreamFinderLogic.saveFirstNote).font(.caption).foregroundStyle(.secondary)
            } else if serverSupportsDiscover == false {
                Text(StreamFinderLogic.olderServerNote).font(.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }
}

/// Asks the camera's recording server for the camera's streams and lets the user pick one.
struct StreamFinderSheet: View {
    @Environment(\.dismiss) private var dismiss
    let api: SentinelAPI
    let cameraID: String
    let cameraName: String
    let mainURL: String
    let currentSub: String
    let username: String?
    let password: String?
    /// The chosen URL goes into the editor's field (saved with the camera).
    let onPick: (String) -> Void
    /// The server answered 404: hide Find streams.
    let onOlderServer: () -> Void

    @State private var discovery: V2.StreamDiscovery?
    @State private var loading = false
    @State private var resizing = false
    @State private var error: String?

    private var rows: [StreamFinderLogic.Row] {
        discovery.map { StreamFinderLogic.rows($0, mainURL: mainURL, currentSub: currentSub) } ?? []
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Streams on \(cameraName)").font(.headline)
            Text("Pick the stream to use as the second (low-res) stream. Changes apply when you save the camera.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            content
            if let error {
                Label(error, systemImage: "exclamationmark.triangle")
                    .font(.caption.weight(.semibold)).foregroundStyle(.red)
                    .fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                Button {
                    Task { await discover() }
                } label: {
                    Label("Look again", systemImage: "arrow.clockwise")
                }
                .disabled(loading || resizing)
                Spacer()
                Button("Close") { dismiss() }
                if let discovery, StreamFinderLogic.canUseRecommended(discovery, mainURL: mainURL, currentSub: currentSub),
                   let rec = StreamFinderLogic.recommendedURL(discovery, mainURL: mainURL) {
                    Button(StreamFinderLogic.useRecommendedButton) { pick(rec) }
                        .buttonStyle(.borderedProminent)
                        .disabled(loading || resizing)
                }
            }
        }
        .padding(20)
        .frame(width: 520)
        .task { await discover() }
    }

    @ViewBuilder private var content: some View {
        if loading && discovery == nil {
            HStack(spacing: 8) {
                ProgressView().controlSize(.small)
                Text("Asking the camera…").font(.callout).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, minHeight: 80)
        } else if let discovery {
            if let banner = StreamFinderLogic.banner(discovery, mainURL: mainURL, currentSub: currentSub) {
                bannerView(banner)
            }
            if rows.isEmpty == false {
                ScrollView {
                    VStack(spacing: 6) {
                        ForEach(rows) { row in rowView(row) }
                    }
                }
                .frame(maxHeight: 280)
            }
        }
    }

    @ViewBuilder private func bannerView(_ banner: StreamFinderLogic.Banner) -> some View {
        switch banner {
        case let .tooSmall(text, button, sub):
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(systemName: "exclamationmark.triangle").foregroundStyle(.orange)
                Text(text).font(.callout).fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: 4)
                Button(resizing ? "Changing…" : button) { Task { await resize(sub) } }
                    .disabled(resizing || loading)
            }
            .padding(10)
            .background(Color.orange.opacity(0.12), in: RoundedRectangle(cornerRadius: 8))
        case let .noneFound(text):
            Label(text, systemImage: "info.circle").font(.callout).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        case let .alreadyUsing(text):
            Label(text, systemImage: "checkmark.circle").font(.callout).foregroundStyle(.secondary)
        }
    }

    private func rowView(_ row: StreamFinderLogic.Row) -> some View {
        Button {
            pick(row.stream.url)
        } label: {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(systemName: row.selected ? "largecircle.fill.circle" : "circle")
                    .foregroundStyle(row.selected ? Color.accentColor : Color.secondary)
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 6) {
                        Text(row.title).font(.callout.weight(row.recommended ? .semibold : .regular))
                        if row.recommended { tag(StreamFinderLogic.recommendedTag, color: .accentColor) }
                        if let badge = row.badge { tag(badge, color: .orange) }
                        if let note = row.roleNote { Text(note).font(.caption).foregroundStyle(.secondary) }
                    }
                    Text(row.stream.url).font(.caption.monospaced()).foregroundStyle(.secondary)
                        .lineLimit(1).truncationMode(.middle)
                }
                Spacer(minLength: 0)
            }
            .padding(8)
            .background(row.selected ? Color.accentColor.opacity(0.08) : Color.secondary.opacity(0.06),
                        in: RoundedRectangle(cornerRadius: 8))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(resizing)
        .help(row.badge == nil ? "Use this stream" : "Found by trying the usual addresses; the camera answered on it")
    }

    private func tag(_ text: String, color: Color) -> some View {
        Text(text)
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 6).padding(.vertical, 1)
            .background(color.opacity(0.18), in: Capsule())
            .foregroundStyle(color)
    }

    private func pick(_ url: String) {
        onPick(url)
        dismiss()
    }

    private func discover() async {
        loading = true
        error = nil
        defer { loading = false }
        do {
            discovery = try await api.discoverStreams(cameraID: cameraID, username: username, password: password)
        } catch {
            if StreamFinderLogic.isOlderServer(error) { onOlderServer() }
            self.error = StreamFinderLogic.errorText(error)
        }
    }

    /// Makes the too-small stream 1280×720, then asks again so the list shows the new size.
    private func resize(_ sub: V2.TooSmallSub) async {
        resizing = true
        error = nil
        do {
            _ = try await api.resizeStream(cameraID: cameraID, encoderToken: sub.encoderToken,
                                           width: StreamFinderLogic.resizeTarget.width,
                                           height: StreamFinderLogic.resizeTarget.height)
            resizing = false
            await discover()
        } catch {
            resizing = false
            self.error = StreamFinderLogic.errorText(error)
        }
    }
}
