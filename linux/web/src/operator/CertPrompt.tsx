// The site's certificate isn't trusted by the system (self-signed, internal
// CA): show it and let the operator pin it for this site only. Nothing else
// about certificate checking changes.
import { useState } from "react";
import { desktop, type PendingCertificate } from "./bridge";
import type { Site } from "./sites";

export default function CertPrompt({ site, cert, onTrusted, onCancel }: { site: Site; cert: PendingCertificate; onTrusted: () => void; onCancel: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const fmt = (t: number) => (t ? new Date(t).toLocaleDateString() : "—");
  return (
    <div className="auth">
      <div className="card op-cert">
        <h1>Check this certificate</h1>
        <p className="lead">
          {site.name} ({cert.host}) presented a certificate this computer doesn't trust ({cert.error.replace(/^net::/, "")}). If it belongs to your
          management server, compare the fingerprint with the one on the server, then trust it for this site.
        </p>
        <div className="stat">
          <span>SHA-256 fingerprint</span>
          <span className="mono op-fingerprint">{cert.fingerprint}</span>
        </div>
        <div className="stat">
          <span>Issued to</span>
          <span>{cert.subject || "—"}</span>
        </div>
        <div className="stat">
          <span>Issued by</span>
          <span>{cert.issuer || "—"}</span>
        </div>
        <div className="stat">
          <span>Valid</span>
          <span>
            {fmt(cert.validFrom)} – {fmt(cert.validTo)}
          </span>
        </div>
        {error && <div className="error op-mt">{error}</div>}
        <button
          className="btn primary op-mt"
          disabled={busy || !desktop}
          onClick={async () => {
            setBusy(true);
            try {
              await desktop!.certificates.trust(site.address, cert.fingerprint);
              onTrusted();
            } catch (e) {
              setError((e as Error).message);
              setBusy(false);
            }
          }}
        >
          Trust this certificate for {site.name}
        </button>
        <button className="btn op-mt" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </div>
  );
}
