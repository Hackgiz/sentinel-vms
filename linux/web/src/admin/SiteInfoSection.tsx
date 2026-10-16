import { useEffect, useState, type FormEvent } from "react";
import { useLoad } from "../shared/hooks";
import { ErrorBanner, Loading } from "./ui";
import { systemApi, type SiteInfo } from "./systemApi";

export default function SiteInfoSection({ canEdit }: { canEdit: boolean }) {
  const site = useLoad(() => systemApi.site.get(), []);
  const [form, setForm] = useState<SiteInfo | null>(null);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    if (site.data) setForm(site.data);
  }, [site.data]);

  async function save(e: FormEvent) {
    e.preventDefault();
    if (!form) return;
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      site.setData(await systemApi.site.put(form));
      setSaved(true);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  }
  const set = (patch: Partial<SiteInfo>) => {
    setSaved(false);
    setForm((f) => (f ? { ...f, ...patch } : f));
  };

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Site information</h1>
          <div className="sub">The name shown at the top of the site tree, and who to contact about this site</div>
        </div>
      </div>
      <ErrorBanner error={site.error ?? error} onRetry={site.error ? site.reload : undefined} />
      {saved && (
        <div className="notice success" role="status">
          Saved.
        </div>
      )}
      {!form ? (
        site.error ? null : (
          <div className="card">
            <Loading />
          </div>
        )
      ) : (
        <form className="card" onSubmit={save} aria-label="Site information">
          <label className="field">
            <span>Site name</span>
            <input type="text" value={form.name} maxLength={80} placeholder="Sentinel" disabled={!canEdit} onChange={(e) => set({ name: e.target.value })} />
          </label>
          <label className="field">
            <span>Description</span>
            <textarea rows={3} value={form.description} maxLength={500} disabled={!canEdit} onChange={(e) => set({ description: e.target.value })} />
          </label>
          <label className="field">
            <span>Contact</span>
            <input type="text" value={form.contact} maxLength={200} placeholder="Name, email or phone" disabled={!canEdit} onChange={(e) => set({ contact: e.target.value })} />
          </label>
          {canEdit && (
            <div className="actions">
              <button className="btn primary" disabled={busy}>
                {busy ? "Saving…" : "Save"}
              </button>
            </div>
          )}
        </form>
      )}
    </>
  );
}
