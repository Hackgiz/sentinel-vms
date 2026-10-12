// A pop-out window (desktop): one camera, or one saved view, filling the
// window. The desktop app remembers where it was on which display.
import { useEffect, useMemo } from "react";
import type { Route } from "./App";
import CameraGrid from "./CameraGrid";
import { useData } from "./data";
import { gridWith, layoutFromView } from "./layout";

export default function Popout({ route }: { route: Exclude<Route, { kind: "main" }> }) {
  const data = useData();
  const view = route.kind === "popout-view" ? data.views.find((v) => v.id === route.viewId) : undefined;
  const camera = route.kind === "popout-camera" ? data.camera(route.cameraId) : undefined;
  const layout = useMemo(() => {
    if (route.kind === "popout-camera") return gridWith(1, 1, [route.cameraId]);
    return view ? layoutFromView(view) : null;
  }, [route, view]);

  useEffect(() => {
    document.title = `${camera?.name ?? view?.name ?? "Sentinel"} · Sentinel Operator`;
  }, [camera, view]);

  let missing = "";
  if (route.kind === "popout-view" && data.viewsLoaded && !view) missing = "This view was deleted.";
  if (route.kind === "popout-camera" && data.cameras && !camera) missing = "This camera isn't available any more.";

  return (
    <div className="op-popout">
      {missing ? (
        <div className="empty">
          <div className="big">⊘</div>
          {missing}
        </div>
      ) : (
        layout && <CameraGrid layout={layout} />
      )}
    </div>
  );
}
