// DemoData.swift
// Sample cameras, alarms and events for Demo Mode (App Review, screenshots,
// trying the app without a server). Shaped exactly like API v2 answers.

import Foundation

@MainActor
enum DemoData {
    static let cameraIDs = ["demo-cam-1", "demo-cam-2", "demo-cam-3", "demo-cam-4", "demo-cam-5", "demo-cam-6"]

    private static func camera(_ index: Int, _ name: String, _ location: String, online: Bool, recording: Bool = true,
                               width: Int = 1920, height: Int = 1080, fps: Int = 30) -> V2.Camera {
        V2.Camera(id: cameraIDs[index], name: name, location: location, rtspUrl: "rtsp://192.168.1.2\(index + 1)/stream",
                  subRtspUrl: "", username: "", hasPassword: false, recording: recording, retentionDays: 0, fps: fps,
                  motionLevel: 2, alertOn: .auto, onvifUrl: "", cameraAnalytics: true, sortOrder: index,
                  createdAt: 0, recorderId: "local", groupIds: [],
                  status: V2.CameraStatus(online: online, viewers: 0, width: width, height: height,
                                          message: online ? "Online" : "No signal"))
    }

    static let cameras: [V2.Camera] = [
        camera(0, "Front Entrance", "Building A", online: true),
        camera(1, "Parking Lot", "North", online: true, width: 2560, height: 1440, fps: 25),
        camera(2, "Loading Dock", "Rear", online: true),
        camera(3, "Server Room", "Floor 2", online: true, width: 1280, height: 720, fps: 15),
        camera(4, "Lobby", "Floor 1", online: false, recording: false),
        camera(5, "Side Gate", "East Fence", online: true)
    ]

    nonisolated private static func ms(_ secondsAgo: Double) -> UnixMillis {
        Date().addingTimeInterval(-secondsAgo).unixMillis
    }

    static var alarms: [V2.Alarm] = {
        func alarm(_ id: String, camera: Int?, kind: String, title: String, detail: String, severity: V2.Severity,
                   state: V2.AlarmState, ago: Double, owner: String = "Unassigned", events: Int = 1,
                   instructions: [V2.Instruction] = []) -> V2.Alarm {
            V2.Alarm(id: id, cameraId: camera.map { cameraIDs[$0] }, cameraName: camera.map { cameras[$0].name },
                     kind: kind, title: title, detail: detail, severity: severity, state: state, owner: owner,
                     createdAt: ms(ago), updatedAt: ms(ago), lastEventAt: ms(ago), eventCount: events,
                     eventId: nil, instructions: instructions, responseLog: [])
        }
        return [
            alarm("demo-alarm-1", camera: 1, kind: "Person", title: "Person detected",
                  detail: "2 people near vehicle row 3", severity: .warning, state: .new, ago: 120, events: 3,
                  instructions: [V2.Instruction(rule: "After-hours lot", text: "Check the lot camera, then call the site manager at 555-0100 if anyone is near the vehicles.")]),
            alarm("demo-alarm-2", camera: 4, kind: "Camera Offline", title: "Camera offline",
                  detail: "Stopped delivering video", severity: .critical, state: .new, ago: 480,
                  instructions: [V2.Instruction(rule: "Recording and camera health", text: "Power-cycle the Lobby PoE port. If it stays down, open a ticket with IT.")]),
            alarm("demo-alarm-3", camera: 0, kind: "Motion", title: "Motion at front door",
                  detail: "Sustained motion 14s", severity: .info, state: .investigating, ago: 600, owner: "Dana", events: 2),
            alarm("demo-alarm-4", camera: 2, kind: "Person", title: "Loitering detected",
                  detail: "Figure stationary near the dock door for 3 min", severity: .warning, state: .acknowledged, ago: 1800, owner: "Eric"),
            alarm("demo-alarm-5", camera: nil, kind: "Storage", title: "Recording disk low",
                  detail: "Recording Server has 18.2 GB free (8%).", severity: .warning, state: .snoozed, ago: 14400)
        ]
    }()

    static var openAlarms: [V2.Alarm] { alarms.filter(\.isOpen) }

    static let events: [V2.Event] = {
        func event(_ id: String, camera: Int, kind: String, score: Double, ago: Double, description: String?) -> V2.Event {
            V2.Event(id: id, cameraId: cameraIDs[camera], kind: kind, label: kind.lowercased(), score: score, box: nil,
                     createdAt: ms(ago), hasThumbnail: false, alarmId: nil, description: description,
                     threat: nil, anomaly: nil, reason: nil, tags: nil)
        }
        return [
            event("demo-event-1", camera: 0, kind: "Person", score: 0.94, ago: 90, description: "A person in a dark jacket carrying a backpack approaches the front door."),
            event("demo-event-2", camera: 1, kind: "Vehicle", score: 0.88, ago: 540, description: "A white SUV pulls into a parking space near row 3."),
            event("demo-event-3", camera: 1, kind: "License Plate", score: 0.79, ago: 600, description: "Plate 7XK A492"),
            event("demo-event-4", camera: 2, kind: "Loitering", score: 1.0, ago: 1500, description: "A figure has remained near the dock door for over three minutes."),
            event("demo-event-5", camera: 5, kind: "Person", score: 0.91, ago: 3200, description: "Two people in hi-vis vests walk past the east fence line.")
        ]
    }()

    /// What a state change does to a demo alarm (the server does this for real sites).
    static func apply(_ change: V2.AlarmStateChange, to alarm: V2.Alarm) -> V2.Alarm {
        var updated = alarm
        updated.state = change.state
        updated.updatedAt = Date().unixMillis
        if change.state != .new { updated.owner = "This iPhone" }
        if let note = change.note { updated.responseLog.append("This iPhone: \(note)") }
        updated.responseLog.append("This iPhone: \(change.state.rawValue)")
        if let i = alarms.firstIndex(where: { $0.id == alarm.id }) { alarms[i] = updated }
        return updated
    }

    static func evidence(for alarm: V2.Alarm) -> V2.Evidence {
        V2.Evidence(id: "demo-evidence-\(alarm.id)", caseId: "HG-DEMO-0001", alarmId: alarm.id,
                    cameraId: alarm.cameraId ?? "", cameraName: alarm.cameraName ?? "Camera",
                    title: "Recorded Segment Export", range: Date().formatted(date: .abbreviated, time: .shortened),
                    size: 24_000_000, sha256: String(repeating: "a1b2c3d4", count: 8),
                    lockedAt: Date().unixMillis, lockedBy: "This iPhone", recorderId: "local")
    }
}
