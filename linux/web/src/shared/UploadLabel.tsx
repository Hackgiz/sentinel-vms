// WAVE23(J): how a video sent from the iPhone shows in evidence lists:
// "Uploaded from iPhone", then who sent it, the device and when it was filmed.
import { Icon } from "./Icons";
import { uploadDetails, uploadedFromLabel, type EvidenceUploadFields } from "./phoneCameras";

export default function UploadLabel({ evidence, fmt, inline }: { evidence: EvidenceUploadFields; fmt?: (ms: number) => string; inline?: boolean }) {
  const details = uploadDetails(evidence, fmt).join(" · ");
  return (
    <>
      <span className="badge blue upload-badge">
        <Icon name="phone" size={12} /> {uploadedFromLabel(evidence.device)}
      </span>
      {inline ? <span className="muted small"> {details}</span> : <div className="muted small">{details}</div>}
    </>
  );
}
