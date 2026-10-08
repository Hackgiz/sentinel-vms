#!/usr/bin/env swift
//
// make-ios-icons.swift
//
// Renders Sentinel Mobile's iOS app icon at every size Xcode lists in
// Asset Catalog → AppIcon.appiconset. Drops the PNGs and an updated
// Contents.json into the appiconset directory.
//
// Modern Xcode (14+) accepts a single 1024×1024 universal icon and downsizes
// at build time — but generating the full set protects against pickier App
// Review automation and older toolchains.
//
// Usage:
//   swift scripts/make-ios-icons.swift "iOS/Sentinel Mobile/Assets.xcassets/AppIcon.appiconset"
//
import AppKit
import Foundation

let args = CommandLine.arguments
guard args.count == 2 else {
    FileHandle.standardError.write(Data("usage: make-ios-icons.swift <AppIcon.appiconset directory>\n".utf8))
    exit(1)
}
let outDir = URL(fileURLWithPath: args[1])
try? FileManager.default.createDirectory(at: outDir, withIntermediateDirectories: true)

// (pixel size, filename, idiom, size_pt, scale) tuples.
// Covers every slot Xcode shows in the AppIcon set.
struct Rendition {
    let pixelSize: Int
    let filename: String
    let idiom: String
    let sizePoints: String
    let scale: String
}

let renditions: [Rendition] = [
    // iPhone notification
    .init(pixelSize: 40,   filename: "icon-20@2x.png",     idiom: "iphone", sizePoints: "20x20",   scale: "2x"),
    .init(pixelSize: 60,   filename: "icon-20@3x.png",     idiom: "iphone", sizePoints: "20x20",   scale: "3x"),
    // iPhone settings
    .init(pixelSize: 58,   filename: "icon-29@2x.png",     idiom: "iphone", sizePoints: "29x29",   scale: "2x"),
    .init(pixelSize: 87,   filename: "icon-29@3x.png",     idiom: "iphone", sizePoints: "29x29",   scale: "3x"),
    // iPhone spotlight
    .init(pixelSize: 80,   filename: "icon-40@2x.png",     idiom: "iphone", sizePoints: "40x40",   scale: "2x"),
    .init(pixelSize: 120,  filename: "icon-40@3x.png",     idiom: "iphone", sizePoints: "40x40",   scale: "3x"),
    // iPhone app
    .init(pixelSize: 120,  filename: "icon-60@2x.png",     idiom: "iphone", sizePoints: "60x60",   scale: "2x"),
    .init(pixelSize: 180,  filename: "icon-60@3x.png",     idiom: "iphone", sizePoints: "60x60",   scale: "3x"),
    // iPad notification
    .init(pixelSize: 20,   filename: "icon-20.png",        idiom: "ipad",   sizePoints: "20x20",   scale: "1x"),
    .init(pixelSize: 40,   filename: "icon-20@2x-ipad.png", idiom: "ipad",  sizePoints: "20x20",   scale: "2x"),
    // iPad settings
    .init(pixelSize: 29,   filename: "icon-29.png",        idiom: "ipad",   sizePoints: "29x29",   scale: "1x"),
    .init(pixelSize: 58,   filename: "icon-29@2x-ipad.png", idiom: "ipad",  sizePoints: "29x29",   scale: "2x"),
    // iPad spotlight
    .init(pixelSize: 40,   filename: "icon-40.png",        idiom: "ipad",   sizePoints: "40x40",   scale: "1x"),
    .init(pixelSize: 80,   filename: "icon-40@2x-ipad.png", idiom: "ipad",  sizePoints: "40x40",   scale: "2x"),
    // iPad app
    .init(pixelSize: 76,   filename: "icon-76.png",        idiom: "ipad",   sizePoints: "76x76",   scale: "1x"),
    .init(pixelSize: 152,  filename: "icon-76@2x.png",     idiom: "ipad",   sizePoints: "76x76",   scale: "2x"),
    // iPad Pro 12.9" app
    .init(pixelSize: 167,  filename: "icon-83.5@2x.png",   idiom: "ipad",   sizePoints: "83.5x83.5", scale: "2x"),
    // App Store
    .init(pixelSize: 1024, filename: "AppIcon-1024.png",   idiom: "ios-marketing", sizePoints: "1024x1024", scale: "1x"),
]

func makeShieldPath(in rect: NSRect) -> NSBezierPath {
    let cr = rect.width * 0.15
    let p = NSBezierPath()
    p.move(to: NSPoint(x: rect.minX + cr, y: rect.maxY))
    p.line(to: NSPoint(x: rect.maxX - cr, y: rect.maxY))
    p.curve(to: NSPoint(x: rect.maxX, y: rect.maxY - cr),
            controlPoint1: NSPoint(x: rect.maxX, y: rect.maxY),
            controlPoint2: NSPoint(x: rect.maxX, y: rect.maxY))
    p.curve(to: NSPoint(x: rect.midX, y: rect.minY),
            controlPoint1: NSPoint(x: rect.maxX, y: rect.midY - rect.height * 0.06),
            controlPoint2: NSPoint(x: rect.midX + rect.width * 0.27, y: rect.minY + rect.height * 0.07))
    p.curve(to: NSPoint(x: rect.minX, y: rect.maxY - cr),
            controlPoint1: NSPoint(x: rect.midX - rect.width * 0.27, y: rect.minY + rect.height * 0.07),
            controlPoint2: NSPoint(x: rect.minX, y: rect.midY - rect.height * 0.06))
    p.curve(to: NSPoint(x: rect.minX + cr, y: rect.maxY),
            controlPoint1: NSPoint(x: rect.minX, y: rect.maxY),
            controlPoint2: NSPoint(x: rect.minX, y: rect.maxY))
    p.close()
    return p
}

// Renders the same artwork as the macOS version, but FLATTENED onto an opaque
// background. Apple rejects iOS App Store icons with VISIBLE transparency, so
// we draw an opaque background fill — the alpha channel in the PNG is still
// uniformly 255 and Apple's submission tooling accepts it.
//
// Note: drawing into a 24-bit RGB NSBitmapImageRep silently produces a black
// PNG (AppKit graphics contexts need an alpha channel to draw correctly), so
// we use a 32-bit RGBA bitmap and rely on the opaque background fill.
func renderIcon(pixelSize: Int) -> Data {
    let dim = CGFloat(pixelSize)
    guard let bitmap = NSBitmapImageRep(
        bitmapDataPlanes: nil,
        pixelsWide: pixelSize, pixelsHigh: pixelSize,
        bitsPerSample: 8, samplesPerPixel: 4,
        hasAlpha: true, isPlanar: false,
        colorSpaceName: .deviceRGB,
        bytesPerRow: pixelSize * 4,
        bitsPerPixel: 32
    ) else { fatalError("could not allocate bitmap rep") }

    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: bitmap)

    let bounds = NSRect(x: 0, y: 0, width: dim, height: dim)

    // Background — opaque, fills the entire square. iOS clips to a rounded
    // squircle automatically; we don't apply our own rounding.
    NSColor(red: 0.055, green: 0.060, blue: 0.068, alpha: 1).setFill()
    NSBezierPath(rect: bounds).fill()

    let shieldRect = bounds.insetBy(dx: dim * 72/512, dy: dim * 56/512)
    let shield = makeShieldPath(in: shieldRect)

    NSColor(red: 0.23, green: 0.64, blue: 0.92, alpha: 0.22).setFill()
    shield.fill()
    shield.lineWidth = max(1, dim * 20 / 512)
    NSColor(red: 0.23, green: 0.64, blue: 0.92, alpha: 1).setStroke()
    shield.stroke()

    let gridInset = shieldRect.width * 0.2
    let gridBottom = shieldRect.height * 0.34
    let gridRect = NSRect(
        x: shieldRect.minX + gridInset,
        y: shieldRect.minY + gridBottom,
        width: shieldRect.width - gridInset * 2,
        height: shieldRect.height - gridBottom - shieldRect.height * 0.12
    )
    let gap = dim * 18 / 512
    let cellW = (gridRect.width - gap) / 2
    let cellH = (gridRect.height - gap) / 2
    for row in 0..<2 {
        for col in 0..<2 {
            let strong = row == col
            let cell = NSRect(
                x: gridRect.minX + CGFloat(col) * (cellW + gap),
                y: gridRect.minY + CGFloat(row) * (cellH + gap),
                width: cellW, height: cellH
            )
            NSColor.white.withAlphaComponent(strong ? 0.92 : 0.36).setFill()
            NSBezierPath(roundedRect: cell, xRadius: max(1, dim * 10 / 512), yRadius: max(1, dim * 10 / 512)).fill()
        }
    }

    NSGraphicsContext.restoreGraphicsState()

    // Flatten to an RGB (no-alpha) CGImage so the PNG matches Apple's
    // requirement that App Store icons not have an alpha channel.
    guard let drawnImage = bitmap.cgImage else {
        fatalError("could not extract CGImage")
    }
    let colorSpace = CGColorSpaceCreateDeviceRGB()
    guard let flatContext = CGContext(
        data: nil,
        width: pixelSize, height: pixelSize,
        bitsPerComponent: 8,
        bytesPerRow: pixelSize * 4,
        space: colorSpace,
        bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue
    ) else { fatalError("could not create flatten context") }
    flatContext.draw(drawnImage, in: CGRect(x: 0, y: 0, width: pixelSize, height: pixelSize))
    guard let flatImage = flatContext.makeImage() else {
        fatalError("could not flatten image")
    }

    let outputData = NSMutableData()
    guard let dest = CGImageDestinationCreateWithData(
        outputData, "public.png" as CFString, 1, nil
    ) else { fatalError("could not create image destination") }
    CGImageDestinationAddImage(dest, flatImage, nil)
    guard CGImageDestinationFinalize(dest) else {
        fatalError("could not encode PNG at size \(pixelSize)")
    }
    return outputData as Data
}

print("Writing \(renditions.count) iOS icon renditions into \(outDir.path)…")
for r in renditions {
    let url = outDir.appendingPathComponent(r.filename)
    try renderIcon(pixelSize: r.pixelSize).write(to: url)
    print("  ✓ \(r.filename)  \(r.pixelSize)×\(r.pixelSize)  \(r.idiom) \(r.sizePoints) @\(r.scale)")
}

// Write a Contents.json that references every rendition.
struct ImageEntry: Encodable {
    let size: String
    let idiom: String
    let filename: String
    let scale: String
}
struct InfoBlock: Encodable {
    let author: String
    let version: Int
}
struct ContentsJSON: Encodable {
    let images: [ImageEntry]
    let info: InfoBlock
}

let contents = ContentsJSON(
    images: renditions.map {
        ImageEntry(size: $0.sizePoints, idiom: $0.idiom, filename: $0.filename, scale: $0.scale)
    },
    info: InfoBlock(author: "xcode", version: 1)
)
let encoder = JSONEncoder()
encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
let json = try encoder.encode(contents)
try json.write(to: outDir.appendingPathComponent("Contents.json"))
print("  ✓ Contents.json")
print("")
print("Done. \(renditions.count) PNGs + Contents.json written.")
