// The Exports workspace: the evidence list (ExportsView) with the long-export
// picker above it, and the "Scheduled" tab. Long exports and schedules need
// the export permission; without it only the evidence list shows.
import { useState } from "react";
import ExportsView from "./ExportsView";
import LongExportPanel from "./LongExportPanel";
import ScheduledExports from "./ScheduledExports";
import { useSigned } from "./session";

type Tab = "evidence" | "scheduled";

export default function ExportsHub() {
  const { can } = useSigned();
  const [tab, setTab] = useState<Tab>("evidence");
  // Bumping the key remounts the evidence list, so a long export that filed a case shows up.
  const [refresh, setRefresh] = useState(0);
  if (!can("exportEvidence")) return <ExportsView />;
  return (
    <>
      <div className="op-subtabs" role="tablist" aria-label="Exports">
        <button role="tab" aria-selected={tab === "evidence"} className={`btn small ${tab === "evidence" ? "primary" : ""}`} onClick={() => setTab("evidence")}>
          Evidence
        </button>{" "}
        <button role="tab" aria-selected={tab === "scheduled"} className={`btn small ${tab === "scheduled" ? "primary" : ""}`} onClick={() => setTab("scheduled")}>
          Scheduled
        </button>
      </div>
      {tab === "evidence" ? (
        <>
          <div className="op-exports op-long-wrap">
            <LongExportPanel onFiled={() => setRefresh((n) => n + 1)} />
          </div>
          <ExportsView key={refresh} />
        </>
      ) : (
        <ScheduledExports />
      )}
    </>
  );
}
