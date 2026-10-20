// Live page, admins only: one line while this server isn't linked to
// Sentinel Cloud (WAVE13-CLOUD-ONBOARDING.md, Stream C). Hidden once linked,
// on a Recording Server (the status answers 404), on any error, and after
// "×" in this browser.
import { useEffect, useState } from "react";
import { cloudApi } from "../admin/cloud";

const DISMISSED = "sentinel_cloud_banner_dismissed";

function wasDismissed(): boolean {
  try {
    return localStorage.getItem(DISMISSED) === "1";
  } catch {
    return false;
  }
}

export default function CloudBanner() {
  const [dismissed, setDismissed] = useState(wasDismissed);
  const [notLinked, setNotLinked] = useState(false);
  useEffect(() => {
    if (dismissed) return;
    let alive = true;
    cloudApi.get().then(
      (s) => alive && setNotLinked(!s.linked),
      () => {},
    );
    return () => {
      alive = false;
    };
  }, [dismissed]);
  if (dismissed || !notLinked) return null;
  return (
    <div className="notice cloud-banner" role="status">
      <span className="grow">
        Watch your cameras from anywhere · <a href="#/cloud">Turn on Sentinel Cloud</a>
      </span>
      <button
        type="button"
        className="btn small"
        aria-label="Dismiss"
        onClick={() => {
          setDismissed(true);
          try {
            localStorage.setItem(DISMISSED, "1");
          } catch {}
        }}
      >
        ×
      </button>
    </div>
  );
}
