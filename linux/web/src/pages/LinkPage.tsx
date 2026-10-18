// /link: approve a device-code sign-in (WAVE10(C)). Sentinel TV shows a
// code; a signed-in person types it here, sees which device asked, and
// lets it in. The device gets a sign-in as this person that can only
// watch (Viewer), limited to the cameras this person may see.
import { useState, type FormEvent } from "react";
import type { User } from "../api";
import type { components } from "../api/v2";
import { v2 } from "../admin/client";
import { ApiError } from "../shared/api";
import { codeFromSearch, formatTyping, isCompleteCode } from "./linkCode";

type DeviceCodeInfo = components["schemas"]["DeviceCodeInfo"];

type Step = { kind: "enter" } | { kind: "confirm"; info: DeviceCodeInfo } | { kind: "done"; approved: boolean; info: DeviceCodeInfo };

export default function LinkPage({ user }: { user: User }) {
  const [code, setCode] = useState(codeFromSearch);
  const [step, setStep] = useState<Step>({ kind: "enter" });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function run<T>(f: () => Promise<T>): Promise<T | undefined> {
    setError("");
    setBusy(true);
    try {
      return await f();
    } catch (e) {
      setError(e instanceof ApiError || e instanceof Error ? e.message : String(e));
      return undefined;
    } finally {
      setBusy(false);
    }
  }

  async function lookup(e: FormEvent) {
    e.preventDefault();
    if (!isCompleteCode(code)) {
      setError("Enter the 8-character code shown on the TV.");
      return;
    }
    const info = await run(() => v2.call("post", "/api/v2/device-codes/lookup", { body: { userCode: code } }));
    if (info) setStep({ kind: "confirm", info });
  }

  async function decide(approve: boolean) {
    const body = { userCode: code };
    const info = await run(() =>
      approve ? v2.call("post", "/api/v2/device-codes/approve", { body }) : v2.call("post", "/api/v2/device-codes/deny", { body }),
    );
    if (info) setStep({ kind: "done", approved: approve, info });
  }

  return (
    <div className="auth">
      <div className="card">
        <div className="logo">
          <img src="/favicon.svg" alt="" />
        </div>
        <h1>Sign in a device</h1>
        {step.kind === "enter" && (
          <form onSubmit={lookup}>
            <p className="lead">Enter the code shown on the TV.</p>
            {error && <div className="error">{error}</div>}
            <label className="field">
              <span>Code</span>
              <input
                type="text"
                value={code}
                onChange={(e) => setCode(formatTyping(e.target.value))}
                placeholder="XXXX-XXXX"
                autoFocus
                autoComplete="off"
                autoCapitalize="characters"
                spellCheck={false}
                style={{ fontSize: 22, letterSpacing: 4, textAlign: "center", fontFamily: "ui-monospace, monospace" }}
              />
            </label>
            <button className="btn primary" disabled={busy || !isCompleteCode(code)}>
              {busy ? "…" : "Continue"}
            </button>
          </form>
        )}
        {step.kind === "confirm" && (
          <>
            <p className="lead">
              <strong>{step.info.deviceName}</strong> wants to sign in as <strong>{user.name}</strong>.
            </p>
            {error && <div className="error">{error}</div>}
            <p style={{ color: "var(--muted)", fontSize: 13, margin: "0 0 18px" }}>
              It will be able to watch live video from the cameras you can see. It can't change settings, acknowledge alarms or
              export video. Only approve a device you can see in front of you. The code expires at{" "}
              {new Date(step.info.expiresAt).toLocaleTimeString()}.
            </p>
            <button className="btn primary" disabled={busy} onClick={() => decide(true)}>
              {busy ? "…" : "Approve"}
            </button>
            <button className="btn danger" style={{ marginTop: 10 }} disabled={busy} onClick={() => decide(false)}>
              Deny
            </button>
          </>
        )}
        {step.kind === "done" && (
          <>
            <p className="lead">
              {step.approved
                ? `${step.info.deviceName} is signed in. It will show your cameras in a few seconds.`
                : `${step.info.deviceName} was refused.`}
            </p>
            <a className="btn" href="/">
              Back to the dashboard
            </a>
          </>
        )}
      </div>
    </div>
  );
}
