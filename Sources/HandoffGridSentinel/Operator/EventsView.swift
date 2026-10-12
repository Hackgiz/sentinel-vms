import AppKit
import SwiftUI
import SentinelCore

/// Detection and analytics events, newest first, live from the event stream,
/// with paging into the past and a jump to playback.
struct EventsView: View {
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var events: EventFeed
    @EnvironmentObject private var cameras: CameraDirectory
    @EnvironmentObject private var navigator: OperatorNavigator

    @State private var cameraFilter: String = ""
    @State private var kindFilter: String = ""

    private var kinds: [String] {
        Array(Set(events.events.map(\.kind))).sorted()
    }

    private var shown: [V2.Event] {
        kindFilter.isEmpty ? events.events : events.events.filter { $0.kind == kindFilter }
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Picker("Camera", selection: $cameraFilter) {
                    Text("All Cameras").tag("")
                    Divider()
                    ForEach(cameras.cameras) { camera in
                        Text(camera.name).tag(camera.id)
                    }
                }
                .frame(maxWidth: 260)
                Picker("Kind", selection: $kindFilter) {
                    Text("All Kinds").tag("")
                    ForEach(kinds, id: \.self) { Text($0).tag($0) }
                }
                .frame(maxWidth: 200)
                Spacer()
                Text("\(shown.count) event\(shown.count == 1 ? "" : "s")").font(.caption).foregroundStyle(.secondary)
                Button {
                    Task { await events.reload(api: runtime.api, cameraID: cameraFilter.isEmpty ? nil : cameraFilter) }
                } label: {
                    Image(systemName: "arrow.clockwise")
                }
                .help("Reload")
            }
            .controlSize(.small)
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .background(SentinelTheme.chrome)
            Divider().overlay(SentinelTheme.line)

            if shown.isEmpty {
                VStack(spacing: 8) {
                    Image(systemName: "bolt.horizontal").font(.largeTitle).foregroundStyle(.secondary)
                    Text(events.isLoading ? "Loading events…" : "No events.").foregroundStyle(.secondary)
                    if let error = events.lastError {
                        Text(error).font(.caption).foregroundStyle(.red)
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                ScrollView {
                    LazyVStack(spacing: 8) {
                        ForEach(shown) { event in
                            EventRow(event: event, cameraName: cameras.name(of: event.cameraId)) {
                                navigator.openPlayback(cameraIDs: [event.cameraId], at: Date(unixMillis: event.createdAt).addingTimeInterval(-5))
                            } openAlarm: {
                                if let alarmID = event.alarmId { navigator.openAlarm(alarmID) }
                            }
                            .onAppear {
                                if event.id == shown.last?.id {
                                    Task { await events.loadMore(api: runtime.api) }
                                }
                            }
                        }
                        if events.isLoading {
                            ProgressView().controlSize(.small).padding()
                        }
                    }
                    .padding(14)
                }
            }
        }
        .onChange(of: cameraFilter) { id in
            Task { await events.reload(api: runtime.api, cameraID: id.isEmpty ? nil : id) }
        }
        .onAppear {
            cameraFilter = events.cameraFilter ?? ""
        }
    }
}

private struct EventRow: View {
    @EnvironmentObject private var runtime: SiteRuntime
    let event: V2.Event
    let cameraName: String
    let play: () -> Void
    let openAlarm: () -> Void

    @State private var image: NSImage?

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            ZStack {
                RoundedRectangle(cornerRadius: 6).fill(Color.black.opacity(0.85))
                if let image {
                    Image(nsImage: image).resizable().aspectRatio(contentMode: .fit)
                        .overlay { boxOverlay }
                } else {
                    Image(systemName: event.hasThumbnail ? "photo" : "bolt.fill").foregroundStyle(.secondary)
                }
            }
            .frame(width: 160, height: 90)
            .clipShape(RoundedRectangle(cornerRadius: 6))

            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 6) {
                    Text(event.label?.isEmpty == false ? event.label!.capitalized : event.kind)
                        .font(.callout.weight(.semibold))
                    if event.score > 0 {
                        MetricPill(text: "\(Int((event.score * 100).rounded()))%")
                    }
                    if let threat = event.threat, threat != "none" {
                        Text("Threat: \(threat)")
                            .font(.caption2.weight(.bold))
                            .foregroundStyle(threat == "high" ? .red : SentinelTheme.amber)
                    }
                    if event.anomaly == true {
                        Text("Unusual").font(.caption2.weight(.bold)).foregroundStyle(SentinelTheme.amber)
                    }
                    Spacer()
                    Text(OperatorFormat.dateTime.string(from: Date(unixMillis: event.createdAt)))
                        .font(.caption).foregroundStyle(.secondary)
                }
                Text("\(cameraName) · \(event.kind)").font(.caption).foregroundStyle(.secondary)
                if let description = event.description, description.isEmpty == false {
                    Text(description).font(.caption).lineLimit(3)
                }
                if let tags = event.tags, tags.isEmpty == false {
                    HStack(spacing: 4) { ForEach(tags.prefix(6), id: \.self) { MetricPill(text: $0) } }
                }
                HStack(spacing: 8) {
                    Button(action: play) { Label("Play", systemImage: "play.fill") }
                    if event.alarmId != nil {
                        Button(action: openAlarm) { Label("Alarm", systemImage: "exclamationmark.octagon") }
                    }
                }
                .controlSize(.small)
            }
        }
        .padding(10)
        .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
        .overlay(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius).stroke(SentinelTheme.line))
        .task(id: event.id) {
            guard event.hasThumbnail else { return }
            image = await runtime.thumbnails.image(for: event.id)
        }
    }

    @ViewBuilder
    private var boxOverlay: some View {
        if let box = event.box, box.count == 4 {
            GeometryReader { proxy in
                let x = box[0] * proxy.size.width
                let y = box[1] * proxy.size.height
                let w = (box[2] - box[0]) * proxy.size.width
                let h = (box[3] - box[1]) * proxy.size.height
                Rectangle()
                    .stroke(SentinelTheme.amber, lineWidth: 1.5)
                    .frame(width: max(w, 2), height: max(h, 2))
                    .offset(x: x, y: y)
            }
        }
    }
}
