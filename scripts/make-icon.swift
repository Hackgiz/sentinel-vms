#!/usr/bin/env swift
//
// make-icon.swift <output-iconset-dir>
//
// Renders the Sentinel VMS app icon (same shield + 2x2 grid the app uses for
// its in-app icon) at every size macOS asks for in a .iconset bundle. Call
// `iconutil -c icns <dir>` after this to produce AppIcon.icns.
//
import AppKit
import Foundation

let args = CommandLine.arguments
guard args.count == 2 else {
    FileHandle.standardError.write(Data("usage: make-icon.swift <output-iconset-dir>\n".utf8))
    exit(1)
}
let outDir = URL(fileURLWithPath: args[1])
try? FileManager.default.createDirectory(at: outDir, withIntermediateDirectories: true)

// (size, filename) pairs required by iconutil.
let renditions: [(Int, String)] = [
    (16,   "icon_16x16.png"),
    (32,   "icon_16x16@2x.png"),
    (32,   "icon_32x32.png"),
    (64,   "icon_32x32@2x.png"),
    (128,  "icon_128x128.png"),
    (256,  "icon_128x128@2x.png"),
    (256,  "icon_256x256.png"),
    (512,  "icon_256x256@2x.png"),
    (512,  "icon_512x512.png"),
    (1024, "icon_512x512@2x.png"),
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

func renderIcon(pixelSize: Int) -> Data {
    let dim = CGFloat(pixelSize)
    let bytesPerRow = pixelSize * 4
    guard let bitmap = NSBitmapImageRep(
        bitmapDataPlanes: nil,
        pixelsWide: pixelSize, pixelsHigh: pixelSize,
        bitsPerSample: 8, samplesPerPixel: 4,
        hasAlpha: true, isPlanar: false,
        colorSpaceName: .deviceRGB,
        bytesPerRow: bytesPerRow,
        bitsPerPixel: 32
    ) else { fatalError("could not allocate bitmap rep") }

    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: bitmap)

    let bounds = NSRect(x: 0, y: 0, width: dim, height: dim)
    // Match in-app icon: dark rounded square background, blue shield outline,
    // soft inner shield fill, 2x2 grid where the diagonal cells pop white.
    NSColor(red: 0.055, green: 0.060, blue: 0.068, alpha: 1).setFill()
    NSBezierPath(roundedRect: bounds, xRadius: dim * 112/512, yRadius: dim * 112/512).fill()

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
    guard let png = bitmap.representation(using: .png, properties: [:]) else {
        fatalError("could not encode PNG at size \(pixelSize)")
    }
    return png
}

for (size, name) in renditions {
    let url = outDir.appendingPathComponent(name)
    try renderIcon(pixelSize: size).write(to: url)
    print("  ✓ \(name) (\(size)×\(size))")
}
