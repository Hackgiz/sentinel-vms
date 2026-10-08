import { useEffect, useMemo, useState } from "react";
import { api, formatBytes, type Camera, type Segment } from "../api";

const dayFmt = new Intl.DateTimeFormat(undefined, { weekday: "long", month: "short", day: "numeric" });
const timeFmt = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" });

export default function RecordingsPage() {
  const [cameras, setCameras] = useState<Camera[]>([]);
  const [cameraID, setCameraID] = useState("");
  const [segments, setSegments] = useState<Segment[] | null>(null);
  const [selected, setSelected] = useState<Segment | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    api
      .cameras()
      .then((c) => {
        setCameras(c);
        setCameraID((id) => id || c[0]?.id || "");
      })
      .catch((e) => setError(e.message));
  }, []);

  useEffect(() => {
    if (!cameraID) return;
    setSegments(null);
    setSelected(null);
    api
      .recordings(cameraID)
      .then((s) => {
        setSegments(s);
        setSelected(s[0] ?? null);
      })
      .catch((e) => setError(e.message));
  }, [cameraID]);

  const byDay = useMemo(() => {
    const groups = new Map<string, Segment[]>();
    for (const s of segments ?? []) {
      const key = dayFmt.format(new Date(s.start * 1000));
      groups.set(key, [...(groups.get(key) ?? []), s]);
    }
    return [...groups.entries()];
  }, [segments]);

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Recordings</h1>
          <div className="sub">15-minute segments, newest first</div>
        </div>
        <select value={cameraID} onChange={(e) => setCameraID(e.target.value)} style={{ width: 240 }}>
          {cameras.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </select>
      </div>
      {error && <div className="error">{error}</div>}
      {cameras.length === 0 ? (
        <div className="card empty">No cameras yet.</div>
      ) : segments && segments.length === 0 ? (
        <div className="card empty">
          <div className="big">▶</div>No recordings for this camera yet. Recording starts as soon as the camera is online.
        </div>
      ) : (
        <div className="rec-layout">
          <div className="card rec-list">
            {byDay.map(([day, list]) => (
              <div key={day}>
                <div className="rec-day">{day}</div>
                {list.map((s) => (
                  <button key={s.name} className={`rec-item ${selected?.name === s.name ? "on" : ""}`} onClick={() => setSelected(s)}>
                    <span>
                      {timeFmt.format(new Date(s.start * 1000))} – {timeFmt.format(new Date(s.end * 1000))}
                      {s.writing && (
                        <span className="badge red" style={{ marginLeft: 8 }}>
                          <span className="dot" /> recording
                        </span>
                      )}
                    </span>
                    <span className="mono">{formatBytes(s.size)}</span>
                  </button>
                ))}
              </div>
            ))}
          </div>
          <div>
            {selected && (
              <>
                <div className="player">
                  <video key={selected.url} src={selected.url} controls autoPlay muted playsInline />
                </div>
                <div className="card" style={{ marginTop: 14, display: "flex", alignItems: "center", gap: 12 }}>
                  <div style={{ flex: 1 }}>
                    <strong>{new Date(selected.start * 1000).toLocaleString()}</strong>
                    <div className="mono">{selected.name}</div>
                  </div>
                  <a className="btn" href={selected.url} download={selected.name}>
                    Download
                  </a>
                </div>
              </>
            )}
          </div>
        </div>
      )}
    </>
  );
}
