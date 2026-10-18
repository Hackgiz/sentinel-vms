import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { createElement } from "react";
import MaskOverlay from "./MaskOverlay";
import { closesPolygon, defaultFrame, exportReencodes, frameSize, polygonArea, polygonProblem, roundPoint, svgPoints, toPoint, type Pt } from "./zones";

const square: Pt[] = [[0.1, 0.1], [0.9, 0.1], [0.9, 0.9], [0.1, 0.9]];

describe("polygonProblem (mirrors internal/store/zones.go)", () => {
  it("accepts simple shapes of any winding", () => {
    expect(polygonProblem(square)).toBeNull();
    expect(polygonProblem([...square].reverse())).toBeNull();
    expect(polygonProblem([[0, 0], [1, 0], [0, 1]])).toBeNull();
    // concave L
    expect(polygonProblem([[0.1, 0.1], [0.5, 0.1], [0.5, 0.5], [0.9, 0.5], [0.9, 0.9], [0.1, 0.9]])).toBeNull();
  });
  it("limits the vertex count to 3-20", () => {
    expect(polygonProblem([[0, 0], [1, 1]])).toMatch(/3 to 20/);
    const ring = (n: number): Pt[] => Array.from({ length: n }, (_, i) => [0.5 + 0.4 * Math.cos((2 * Math.PI * i) / n), 0.5 + 0.4 * Math.sin((2 * Math.PI * i) / n)]);
    expect(polygonProblem(ring(20))).toBeNull();
    expect(polygonProblem(ring(21))).toMatch(/3 to 20/);
  });
  it("rejects points outside the picture and repeated points", () => {
    expect(polygonProblem([[0, 0], [1.2, 0], [0, 1]])).toMatch(/outside/);
    expect(polygonProblem([[0, 0], [Number.NaN, 0], [0, 1]])).toMatch(/outside/);
    expect(polygonProblem([[0, 0], [0, 0], [1, 1], [0, 1]])).toMatch(/same place/);
  });
  it("rejects bow-ties, crossings, touches and fold-backs", () => {
    expect(polygonProblem([[0.1, 0.1], [0.9, 0.9], [0.9, 0.1], [0.1, 0.9]])).toMatch(/crosses/);
    expect(polygonProblem([[0.1, 0.1], [0.9, 0.1], [0.9, 0.9], [0.3, 0.9], [0.3, 0]])).toMatch(/crosses/);
    expect(polygonProblem([[0.1, 0.1], [0.9, 0.1], [0.9, 0.9], [0.5, 0.1], [0.1, 0.9]])).toMatch(/crosses/);
    expect(polygonProblem([[0.1, 0.1], [0.9, 0.1], [0.5, 0.1], [0.5, 0.9]])).toMatch(/folds back/);
    expect(polygonProblem([[0, 0], [0.5, 0.5], [1, 1]])).toMatch(/folds back/);
  });
  it("rejects slivers with no area", () => {
    expect(polygonProblem([[0, 0], [1, 0], [1, 1e-7]])).toMatch(/no area/);
  });
  it("computes area by the shoelace formula", () => {
    expect(polygonArea(square)).toBeCloseTo(0.64);
    expect(polygonArea([[0, 0], [1, 0], [0, 1]])).toBeCloseTo(0.5);
  });
});

describe("editor helpers", () => {
  it("maps a pointer to a clamped normalized point", () => {
    const rect = { left: 100, top: 50, width: 400, height: 200 };
    expect(toPoint(300, 150, rect)).toEqual([0.5, 0.5]);
    expect(toPoint(0, 1000, rect)).toEqual([0, 1]);
  });
  it("closes a polygon only near its first point and with 3+ points", () => {
    expect(closesPolygon(square.slice(0, 2), [0.1, 0.1])).toBe(false);
    expect(closesPolygon(square, [0.11, 0.11])).toBe(true);
    expect(closesPolygon(square, [0.5, 0.5])).toBe(false);
  });
  it("rounds and serializes points", () => {
    expect(roundPoint([0.123456, 0.987654])).toEqual([0.1235, 0.9877]);
    expect(svgPoints([[0.5, 0.25], [1, 1]], 200, 100)).toBe("100,25 200,100");
  });
  it("overlays on the stream's size, else 16:9", () => {
    expect(frameSize({ width: 640, height: 480 })).toEqual({ width: 640, height: 480 });
    expect(frameSize({ width: 0, height: 0 })).toEqual(defaultFrame);
    expect(frameSize(undefined)).toEqual(defaultFrame);
  });
  it("re-encodes exports only for cameras with masks", () => {
    expect(exportReencodes(undefined)).toBe(false);
    expect(exportReencodes([])).toBe(false);
    expect(exportReencodes([square])).toBe(true);
  });
});

describe("MaskOverlay", () => {
  it("draws nothing without masks", () => {
    expect(renderToStaticMarkup(createElement(MaskOverlay, { camera: { masks: [] } }))).toBe("");
    expect(renderToStaticMarkup(createElement(MaskOverlay, { camera: {} }))).toBe("");
  });
  it("draws one black polygon per mask, scaled to the stream size", () => {
    const html = renderToStaticMarkup(
      createElement(MaskOverlay, {
        camera: { masks: [[[0, 0], [0.5, 0], [0.5, 0.5]], [[0.5, 0.5], [1, 0.5], [1, 1]]], status: { width: 200, height: 100 } },
      }),
    );
    expect(html).toContain('viewBox="0 0 200 100"');
    expect(html).toContain('preserveAspectRatio="xMidYMid meet"');
    expect(html.match(/<polygon/g)).toHaveLength(2);
    expect(html).toContain('points="0,0 100,0 100,50"');
    expect(html).toContain('fill="#000"');
  });
});
