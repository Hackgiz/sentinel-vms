// One slim, dismissible notification strip under the tab bar, fed by the
// notice bus (notices.ts).
import { useEffect, useState } from "react";
import { addNotice, autoDismissMs, autoDismisses, dismissNotice, subscribeNotices, type Notice } from "./notices";

export default function Banners() {
  const [list, setList] = useState<Notice[]>([]);
  useEffect(() => subscribeNotices((n) => setList((l) => addNotice(l, n))), []);
  useEffect(() => {
    const timers = list.filter(autoDismisses).map((n) => setTimeout(() => setList((l) => dismissNotice(l, n.id)), Math.max(0, n.at + autoDismissMs - Date.now())));
    return () => timers.forEach(clearTimeout);
  }, [list]);
  if (!list.length) return null;
  return (
    <div className="op-banners" role="region" aria-label="Notifications">
      {list.map((n) => (
        <div key={n.id} className={`op-banner ${n.kind}`} role={n.kind === "warn" || n.kind === "error" ? "alert" : "status"}>
          <span className="grow">{n.text}</span>
          <button className="btn small" aria-label="Dismiss" title="Dismiss" onClick={() => setList((l) => dismissNotice(l, n.id))}>
            ✕
          </button>
        </div>
      ))}
    </div>
  );
}
