import { frameSize, svgPoints, type Pt } from "./zones";
import "./zones.css";

/**
 * Black polygons over a camera's video for its privacy masks. Display-level
 * only (the server burns masks into JPEGs and exports). The SVG fills the
 * tile and its viewBox is the stream's pixel size, so with
 * preserveAspectRatio "meet" it scales exactly like the video's
 * object-fit: contain.
 */
export default function MaskOverlay({ camera }: { camera: { masks?: number[][][]; status?: { width?: number; height?: number } } }) {
  const masks = camera.masks ?? [];
  if (masks.length === 0) return null;
  const { width, height } = frameSize(camera.status);
  return (
    <svg className="mask-overlay" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="xMidYMid meet" aria-hidden="true" data-testid="mask-overlay">
      {masks.map((m, i) => (
        <polygon key={i} points={svgPoints(m as Pt[], width, height)} fill="#000" />
      ))}
    </svg>
  );
}
