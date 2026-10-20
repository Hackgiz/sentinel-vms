import SwiftUI
import SentinelCore

/// Black polygons over a camera's video for its privacy masks (Wave 11,
/// stream A; the web's `MaskOverlay.tsx`). Display-level only: the server
/// burns masks into snapshots and exports. Placed with `MaskGeometry` over
/// the rect the video actually occupies, so it follows the tile's
/// letterboxing (`fill: false`) or crop (`fill: true`).
struct MaskOverlayView: View {
    let camera: V2.Camera?
    var fill = false

    var body: some View {
        if let camera, MaskGeometry.polygons(camera.masks).isEmpty == false {
            Canvas { ctx, size in
                let frame = MaskGeometry.frameSize(width: camera.status.width, height: camera.status.height)
                for polygon in MaskGeometry.layout(masks: camera.masks, container: size, frame: frame, gravity: fill ? .fill : .fit) {
                    var path = Path()
                    path.addLines(polygon)
                    path.closeSubpath()
                    ctx.fill(path, with: .color(.black))
                }
            }
            .allowsHitTesting(false)
            .accessibilityHidden(true)
        }
    }
}
