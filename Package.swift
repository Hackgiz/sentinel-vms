// swift-tools-version: 5.9

import PackageDescription

let package = Package(
    name: "HandoffGridSentinel",
    platforms: [
        .macOS(.v13),
        .iOS(.v16)
    ],
    products: [
        .executable(
            name: "HandoffGridSentinel",
            targets: ["HandoffGridSentinel"]
        ),
        .library(
            name: "SentinelCore",
            targets: ["SentinelCore"]
        )
    ],
    targets: [
        .target(
            name: "SentinelCore"
        ),
        .executableTarget(
            name: "HandoffGridSentinel",
            dependencies: ["SentinelCore"],
            exclude: ["Info.plist", "HandoffGridSentinel.entitlements", "PrivacyInfo.xcprivacy"],
            linkerSettings: [
                .unsafeFlags([
                    "-Xlinker", "-sectcreate",
                    "-Xlinker", "__TEXT",
                    "-Xlinker", "__info_plist",
                    "-Xlinker", "Sources/HandoffGridSentinel/Info.plist"
                ])
            ]
        ),
        .testTarget(
            name: "SentinelOperatorTests",
            dependencies: ["SentinelCore"]
        ),
        .testTarget(
            name: "HandoffGridSentinelTests",
            dependencies: ["SentinelCore"]
        )
    ]
)
