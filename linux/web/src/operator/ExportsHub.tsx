// The Exports workspace: the evidence list (ExportsView) with the long-export
// picker above it, and the "Scheduled" tab. Long exports and schedules need
// the export permission; without it only the evidence list shows.
import { useState } from "react";
import ExportsView from "./ExportsView";
import LongExportPanel from "./LongExportPanel";
import ScheduledExports from "./ScheduledExports";
import { useSigned } from "./session";
import { PageHeader } from "../shared/Cards";

type Tab = "evidence" | "scheduled";

export default function ExportsHub() {
  const { can } = useSigned();
  const [tab, setTab] = useState<Tab>("evidence");
  // Bumping the key remounts the evidence list, so a long export that filed a case shows up.
  const [refresh, setRefresh] = useState(0);
  const header = (tools?: React.ReactNode) => (
    <div className="op-exports-head">
      <PageHeader title="Exports" subtitle={tab === "scheduled" ? "Exports that run on a schedule" : "Footage locked as evidence"}>
        {tools}
      </PageHeader>
    </div>
  );
  if (!can("exportEvidence"))
    return (
      <>
        {header()}
        <ExportsView />
      </>
    );
  return (
    <>
      {header(
        <div className="seg op-subtabs" role="tablist" aria-label="Exports">
          <button role="tab" aria-selected={tab === "evidence"} className={tab === "evidence" ? "on" : ""} onClick={() => setTab("evidence")}>
            Evidence
          </button>
          <button role="tab" aria-selected={tab === "scheduled"} className={tab === "scheduled" ? "on" : ""} onClick={() => setTab("scheduled")}>
            Scheduled
          </button>
        </div>,
      )}
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
