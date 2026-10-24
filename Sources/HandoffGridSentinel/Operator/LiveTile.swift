import AppKit
import AVFoundation
import SwiftUI
import SentinelCore

/// One camera's live video with its name, state and alarm ring.
struct LiveTile: View {
    @EnvironmentObject private var runtime: SiteRuntime
    @EnvironmentObject private var alarms: AlarmQueue
    let camera: V2.Camera
    var isSelected = false
    var fill = false
    var showsControls = true
    @StateObject private var status = LivePlayerStatus()

    var body: some View {
        ZStack {
            Color.black
            if camera.status.online {
                LiveVideoView(cameraID: camera.id, broker: runtime.video, gravity: fill ? .resizeAspectFill : .resizeAspect, status: status)
            }
        }
        .overlay { if camera.status.online { MaskOverlayView(camera: camera, fill: fill) } } // WAVE11(A): privacy masks
        .overlay(alignment: .top) { header }
        .overlay(alignment: .bottom) { footer }
        .overlay { stateOverlay }
        .overlay { TileReconnectingOverlay(isOffline: camera.status.online == false) } // tile health (wave 5)
        .overlay(alignment: .trailing) { if isSelected, showsControls, camera.status.online { PTZOverlayView(camera: camera) } }
        .overlay(alignment: .bottom) { if isSelected, showsControls, camera.status.online { TalkButton(camera: camera) } } // WAVE11(D): hold to talk
        .overlay {
            if let severity = worstOpenAlarm {
                PulsingAlertRing(cornerRadius: SentinelMetrics.cardRadius, color: severity.tint)
            } else {
                RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius)
                    .stroke(isSelected ? SentinelTheme.accent : Color.white.opacity(0.08), lineWidth: isSelected ? 2 : 1)
            }
        }
        .clipShape(RoundedRectangle(cornerRadius: SentinelMetrics.cardRadius))
        .contentShape(Rectangle())
    }

    private var worstOpenAlarm: AlertSeverity? {
        alarms.open.filter { $0.cameraId == camera.id }.map(\.severity).min { $0.rank < $1.rank }
    }

    private var header: some View {
        HStack(alignment: .top) {
            VStack(alignment: .leading, spacing: 2) {
                Text(camera.name).font(.system(size: 11, weight: .semibold)).foregroundStyle(.white).lineLimit(1)
                if camera.location.isEmpty == false {
                    Text(camera.location).font(.system(size: 9)).foregroundStyle(.white.opacity(0.65)).lineLimit(1)
                }
            }
            Spacer()
            if showsControls {
                Button {
                    OperatorWindows.shared.openCamera(camera.id, name: camera.name)
                } label: {
                    Image(systemName: "macwindow.on.rectangle")
                        .font(.system(size: 10, weight: .semibold))
                        .foregroundStyle(.white)
                        .padding(6)
                        .background(.black.opacity(0.55), in: Circle())
                }
                .buttonStyle(.plain)
                .help("Pop out into its own window")
            }
            StatusBadge(status: camera.status.online ? .online : .offline)
        }
        .padding(.horizontal, 10)
        .padding(.top, 8)
        .padding(.bottom, 18)
        .background(LinearGradient(colors: [.black.opacity(0.72), .clear], startPoint: .top, endPoint: .bottom))
    }

    private var footer: some View {
        HStack(spacing: 6) {
            if camera.recording {
                HStack(spacing: 4) {
                    Circle().fill(Color.red).frame(width: 6, height: 6)
                    Text("REC").font(.system(size: 8, weight: .bold)).foregroundStyle(.white)
                }
                .padding(.horizontal, 6).padding(.vertical, 3)
                .background(.black.opacity(0.55), in: Capsule())
            }
            if camera.isPhone { PhoneCameraBadge() } // wave 23
            TileActivityGlyph(tracker: runtime.activity, cameraID: camera.id) // tile health (wave 5)
            MetricPill(text: camera.resolutionLabel)
            Spacer()
            if status.route.isEmpty == false, camera.status.online {
                MetricPill(text: status.route.uppercased())
                    .help(status.route == "Gateway" ? "Video goes through the Management Server" : "Video comes straight from the Recording Server")
            }
        }
        .padding(.horizontal, 10)
        .padding(.bottom, 8)
    }

    @ViewBuilder
    private var stateOverlay: some View {
        if camera.status.online == false {
            message(PhoneCameraLogic.offlineMessage(camera), // wave 23: "Phone not streaming" for phone cameras
                    symbol: PhoneCameraLogic.needsCloud(camera) ? "cloud" : camera.isPhone ? "iphone.slash" : "video.slash.fill",
                    color: PhoneCameraLogic.needsCloud(camera) ? SentinelTheme.amber : .red) // addendum 2
        } else {
            switch status.state {
            case .connecting:
                ProgressView().controlSize(.small).tint(.white)
            case .failed(let text):
                message(text, symbol: "arrow.triangle.2.circlepath", color: SentinelTheme.amber)
            case .playing:
                EmptyView()
            }
        }
    }

    private func message(_ text: String, symbol: String, color: Color) -> some View {
        VStack(spacing: 6) {
            Image(systemName: symbol).font(.system(size: 14, weight: .semibold)).foregroundStyle(color)
            Text(text).font(.caption2.weight(.semibold)).foregroundStyle(color)
                .multilineTextAlignment(.center).lineLimit(3)
        }
        .padding(10)
        .background(.black.opacity(0.72), in: RoundedRectangle(cornerRadius: 6))
        .padding(12)
    }
}
