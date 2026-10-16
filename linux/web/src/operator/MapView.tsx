// The floorplan map: pick a plan, see its cameras pinned on it. A pin shows
// the camera's name on hover, is grey when the camera is offline and pulses
// red while an alarm is open on it. Clicking opens the camera live; with an
// open alarm it jumps to the camera's recording at that alarm. State comes
// from the workspace data context (event stream) — no polling here.
import { useCallback, useEffect, useState } from "react";
import { errorMessage, type Floorplan } from "../shared/api";
import { alarmOn, pinState, usePlanImage } from "../shared/floorplan";
import "../admin/floorplans.css";
import { useData } from "./data";
import { useSigned } from "./session";
import type { PlaybackRequest } from "./Workspace";

export default function MapView({ onOpenCamera, onPlayback }: { onOpenCamera: (cameraId: string) => void; onPlayback: (r: PlaybackRequest) => void }) {
  const { session } = useSigned();
  const client = session.client;
  const data = useData();
  const [plans, setPlans] = useState<Floorplan[] | null>(null);
  const [error, setError] = useState("");
  const [pickedId, setPickedId] = useState<string | null>(null);

  const load = useCallback(() => {
    client.floorplans
      .list()
      .then((p) => {
        setPlans(p);
        setError("");
      })
      .catch((e) => setError(errorMessage(e)));
  }, [client]);
  useEffect(load, [load]);
  // Plans and pins change with the configuration; the stream tells us.
  useEffect(
    () =>
      data.subscribe((m) => {
        if (m.type === "config.changed" || m.type === "lagged") load();
      }),
    [data, load],
  );

  const plan = plans?.find((p) => p.id === pickedId) ?? plans?.[0] ?? null;
  const image = usePlanImage((signal) => client.floorplans.image(plan!.id, signal), plan ? `${plan.id}:${plan.updatedAt}` : "none");

  if (plans === null) {
    return <div className="op-map muted op-pad">{error ? <span className="error">{error}</span> : "Loading the map…"}</div>;
  }
  if (!plan) {
    return <div className="op-map muted op-pad">{error ? <span className="error">{error}</span> : "No floorplans yet. An administrator can upload one under Admin, Floorplans."}</div>;
  }

  const alarmCount = plan.pins.filter((p) => alarmOn(data.alarms, p.cameraId)).length;
  return (
    <div className="op-map" aria-label="Floorplan map">
      <div className="op-map-bar">
        {plans.length > 1 ? (
          <select value={plan.id} onChange={(e) => setPickedId(e.target.value)} aria-label="Floorplan">
            {plans.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        ) : (
          <strong>{plan.name}</strong>
        )}
        <span className="muted small grow">
          {plan.pins.length} camera{plan.pins.length === 1 ? "" : "s"}
        </span>
        {alarmCount > 0 && <span className="badge red">{alarmCount} with an open alarm</span>}
      </div>
      {error && <div className="error">{error}</div>}
      <div className="fp-plan" style={{ aspectRatio: `${plan.width} / ${plan.height}`, width: `min(100%, calc(72vh * ${plan.width / plan.height}))` }}>
        {image ? <img src={image} alt={`Floorplan ${plan.name}`} draggable={false} /> : <div className="muted op-pad">Loading the plan…</div>}
        {plan.pins.map((p) => {
          const cam = data.camera(p.cameraId);
          const alarm = alarmOn(data.alarms, p.cameraId);
          const state = pinState(cam, data.alarms, p.cameraId);
          const name = cam?.name ?? "Camera";
          const note = state === "alarm" ? `${alarm!.title} — click to review` : state === "offline" ? "Offline" : "Click to open";
          return (
            <button
              key={p.cameraId}
              type="button"
              className={`fp-pin ${state}`}
              style={{ left: `${p.x * 100}%`, top: `${p.y * 100}%` }}
              title={`${name}: ${note}`}
              aria-label={`${name}, ${state === "alarm" ? "alarm" : state === "offline" ? "offline" : "online"}`}
              onClick={() => (alarm ? onPlayback({ cameraId: p.cameraId, at: alarm.createdAt - 10_000 }) : onOpenCamera(p.cameraId))}
            >
              {p.heading >= 0 && <span className="fp-cone" style={{ transform: `rotate(${p.heading}deg)` }} />}
              <span className="fp-dot" />
              <span className="fp-label">{name}</span>
            </button>
          );
        })}
      </div>
    </div>
  );
}
