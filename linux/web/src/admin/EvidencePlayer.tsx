// WAVE23(J): play an evidence file in place (exports and uploaded videos alike).
import { Modal } from "./ui";

export default function EvidencePlayer({ title, src, onClose }: { title: string; src: string; onClose: () => void }) {
  return (
    <Modal
      title={title}
      wide
      onClose={onClose}
      onSubmit={onClose}
      actions={
        <button className="btn primary" autoFocus>
          Close
        </button>
      }
    >
      <div className="evidence-player">
        <video src={src} controls autoPlay playsInline />
      </div>
    </Modal>
  );
}

/** The download file name: the case id, with the uploaded file's own extension when it had one. */
export function evidenceFileName(caseId: string, fileName?: string): string {
  const ext = /\.(mp4|mov|m4v)$/i.exec(fileName ?? "")?.[1]?.toLowerCase() ?? "mp4";
  return `${caseId}.${ext}`;
}
