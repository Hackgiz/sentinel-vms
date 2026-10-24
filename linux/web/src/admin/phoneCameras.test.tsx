// WAVE23(J): iPhone cameras in Admin → Cameras (editor, list, key), live tiles, and uploaded evidence.
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { createClient, type RecordingServer } from "../shared/api";
import {
  offlineText,
  phoneStatus,
  phoneStatusLine,
  toMs,
  uploadDetails,
  uploadedFromLabel,
  type SourcedCamera,
  type SourcedEvidence,
  type SourcedEvidenceCase,
} from "../shared/phoneCameras";
import { recordingDefaults, recordingFormOf, type RecCamera } from "../shared/recordingModes";
import VideoTile from "../shared/VideoTile";
import { CaseSubject } from "../operator/ExportsView";
import { CameraEditor, PhoneBadge, PushKeyPanel, StatusCell, runKeyAction } from "./CamerasSection";
import { EvidenceTable } from "./EvidenceSection";
import { evidenceFileName } from "./EvidencePlayer";
import RecordingFields from "./RecordingFields";

const now = new Date(2026, 9, 23, 15, 0).getTime();
const at1402 = new Date(2026, 9, 23, 14, 2).getTime();

const camera = (over: Partial<RecCamera & SourcedCamera> = {}) =>
  ({
    id: "c1",
    name: "Porch phone",
    location: "Porch",
    rtspUrl: "",
    subRtspUrl: "",
    username: "",
    hasPassword: false,
    recording: true,
    retentionDays: 0,
    motionLevel: 2,
    alertOn: "auto",
    onvifUrl: "",
    cameraAnalytics: false,
    recorderId: "local",
    groupIds: [],
    sortOrder: 0,
    recordingMode: "default",
    recordTrigger: "default",
    preRollSeconds: 0,
    postRollSeconds: 0,
    lowResRetentionDays: 0,
    status: { online: false, viewers: 0, message: "Phone not streaming" },
    source: "phone",
    ...over,
  }) as unknown as RecCamera & SourcedCamera;

const servers = [{ id: "local", name: "Workstation", local: true, revoked: false, online: true }] as unknown as RecordingServer[];

const editor = (c: (RecCamera & SourcedCamera) | null, initialKind?: "rtsp" | "phone") =>
  renderToStaticMarkup(
    <CameraEditor camera={c} servers={servers} groups={[]} recDefaults={recordingDefaults} ingest={null} onClose={() => {}} onSaved={() => {}} initialKind={initialKind} />,
  );

describe("phone camera status", () => {
  it("says streaming, not streaming with last seen, never connected, or key revoked", () => {
    expect(phoneStatus(camera({ status: { online: true, viewers: 0, message: "" }, phoneDevice: "EJ's iPhone" }), now)).toEqual({
      streaming: true,
      state: "Streaming",
      detail: "from EJ's iPhone",
    });
    expect(phoneStatusLine(camera({ lastSeenAt: at1402 }), now)).toBe("iPhone camera · Not streaming · last seen 14:02");
    expect(phoneStatusLine(camera({ lastSeenAt: new Date(at1402).toISOString() } as unknown as Parameters<typeof camera>[0]), now)).toBe("iPhone camera · Not streaming · last seen 14:02");
    expect(phoneStatusLine(camera(), now)).toBe("iPhone camera · Not streaming · never connected");
    expect(phoneStatus(camera({ hasPushKey: false }), now).state).toBe("Key revoked");
  });

  it("reads times as ms, s or RFC 3339", () => {
    expect(toMs(at1402)).toBe(at1402);
    expect(toMs(at1402 / 1000)).toBe(at1402);
    expect(toMs(null)).toBe(0);
    expect(toMs("nope")).toBe(0);
  });

  it("gives phone tiles their own offline text", () => {
    expect(offlineText(camera())).toBe("Phone not streaming");
    expect(offlineText({ status: { message: "No signal" } })).toBe("No signal");
    expect(offlineText({})).toBe("Offline");
  });
});

describe("camera list", () => {
  it("shows the phone badge and Streaming / Not streaming · last seen", () => {
    expect(renderToStaticMarkup(<PhoneBadge />)).toContain("iPhone");
    const off = renderToStaticMarkup(<StatusCell camera={camera({ lastSeenAt: at1402 })} now={now} />);
    expect(off).toContain("badge red");
    expect(off).toContain("Not streaming");
    expect(off).toContain("last seen 14:02");
    const on = renderToStaticMarkup(<StatusCell camera={camera({ status: { online: true, viewers: 1, message: "Streaming from EJ's iPhone" } })} now={now} />);
    expect(on).toContain("badge green");
    expect(on).toContain("Streaming");
  });

  it("leaves RTSP cameras as they were", () => {
    const html = renderToStaticMarkup(<StatusCell camera={camera({ source: "rtsp", status: { online: false, viewers: 0, message: "No signal" } })} />);
    expect(html).toContain("No signal");
    expect(html).not.toContain("streaming");
  });
});

describe("phone camera editor", () => {
  it("hides the RTSP, ONVIF, second-stream and Find streams fields", () => {
    const html = editor(camera({ lastSeenAt: at1402 }));
    for (const gone of ["Main stream (RTSP)", "Second stream", "Find streams", "ONVIF address", "Username", "Password", "built-in analytics"]) expect(html).not.toContain(gone);
    for (const kept of ["Name", "Location", "Groups", "Recording", "Recording server", "Motion detection", "iPhone camera · Not streaming"]) expect(html).toContain(kept);
  });

  it("shows dual disabled: not available for iPhone cameras", () => {
    const html = editor(camera());
    expect(html).toMatch(/disabled="" name="recordingMode" value="dual"/);
    expect(html).toContain("Not available for iPhone cameras.");
    expect(html).not.toContain("Needs a second stream");
  });

  it("keeps dual unavailable even if a sub-stream is set", () => {
    const html = renderToStaticMarkup(
      <RecordingFields form={recordingFormOf(null)} onChange={() => {}} hasSub phone motionLevel={2} analytics={false} server={{ ...recordingDefaults, defaultMode: "dual" }} />,
    );
    expect(html).toMatch(/disabled="" name="recordingMode" value="dual"/);
    expect(html).toContain("iPhone cameras have no second stream");
  });

  it("offers Rotate key and Revoke key", () => {
    const html = editor(camera());
    expect(html).toContain("Rotate key…");
    expect(html).toContain("Revoke key…");
  });

  it("still shows RTSP fields for a network camera", () => {
    const html = editor(camera({ source: "rtsp", rtspUrl: "rtsp://cam/main" }));
    expect(html).toContain("Main stream (RTSP)");
    expect(html).toContain("ONVIF address");
    expect(html).not.toContain("Rotate key");
  });
});

describe("Add camera → Use an iPhone", () => {
  it("offers both kinds and explains the iPhone steps with nothing to type", () => {
    const rtsp = editor(null);
    expect(rtsp).toContain("Network camera");
    expect(rtsp).toContain("Use an iPhone");
    expect(rtsp).toContain("Main stream (RTSP)");
    const phone = editor(null, "phone");
    expect(phone).toContain("Open Sentinel on the iPhone.");
    expect(phone).toContain("Settings → Use this iPhone as a camera.");
    expect(phone).toContain("Nothing to type here.");
    expect(phone).not.toContain("Main stream (RTSP)");
    expect(phone).toContain(">Done<");
  });
});

describe("Sentinel Cloud (Addendum 2)", () => {
  const lapsed = camera({ status: { online: false, viewers: 0, message: "Needs Sentinel Cloud" }, lastSeenAt: at1402 });

  it("the Use an iPhone explanation says it needs Sentinel Cloud, with Learn more", () => {
    const html = renderToStaticMarkup(<CameraEditor camera={null} servers={servers} groups={[]} recDefaults={recordingDefaults} ingest={null} onClose={() => {}} onSaved={() => {}} initialKind="phone" />);
    expect(html).toContain("Needs Sentinel Cloud.");
    expect(html).toContain('href="https://sentvms.com/cloud"');
    expect(html).toContain(">Learn more<");
    expect(html).not.toContain("This site isn");
  });

  it("marks the option when the site's plan has no phone cameras", () => {
    const html = renderToStaticMarkup(
      <CameraEditor camera={null} servers={servers} groups={[]} recDefaults={recordingDefaults} ingest={null} onClose={() => {}} onSaved={() => {}} phoneAvailable={false} />,
    );
    expect(html).toContain("Use an iPhone");
    expect(html).toContain("· Needs Sentinel Cloud");
    const setup = renderToStaticMarkup(
      <CameraEditor camera={null} servers={servers} groups={[]} recDefaults={recordingDefaults} ingest={null} onClose={() => {}} onSaved={() => {}} initialKind="phone" phoneAvailable={false} />,
    );
    expect(setup).toContain("This site isn&#x27;t on Sentinel Cloud, so the iPhone can&#x27;t stream yet.");
    expect(editor(null)).not.toContain("· Needs Sentinel Cloud");
  });

  it("a lapsed phone camera says Needs Sentinel Cloud instead of Phone not streaming", () => {
    expect(offlineText(lapsed)).toBe("Needs Sentinel Cloud");
    expect(phoneStatusLine(lapsed, now)).toBe("iPhone camera · Needs Sentinel Cloud · last seen 14:02");
    const cell = renderToStaticMarkup(<StatusCell camera={lapsed} now={now} />);
    expect(cell).toContain("Needs Sentinel Cloud");
    expect(cell).not.toContain("Not streaming");
    const tile = renderToStaticMarkup(<VideoTile client={createClient()} camera={lapsed} />);
    expect(tile).toContain("Needs Sentinel Cloud");
    expect(tile).not.toContain("Phone not streaming");
  });

  it("the editor shows it with the Learn more link", () => {
    const html = editor(lapsed);
    expect(html).toContain("iPhone camera · Needs Sentinel Cloud");
    expect(html).toContain('href="https://sentvms.com/cloud"');
    expect(editor(camera())).not.toContain("sentvms.com/cloud");
  });
});

describe("push key", () => {
  const calls: { method: string; url: string }[] = [];
  const client = createClient({
    fetch: async (input, init) => {
      const url = String(input);
      calls.push({ method: init?.method ?? "GET", url });
      if (url.includes("denied")) return new Response(JSON.stringify({ error: { code: "forbidden", message: "Not allowed." } }), { status: 403, headers: { "Content-Type": "application/json" } });
      return init?.method === "POST" ? new Response(JSON.stringify({ pushKey: "secret" }), { status: 200, headers: { "Content-Type": "application/json" } }) : new Response(null, { status: 204 });
    },
  });

  it("asks before rotating, and says the phone must be set up again", () => {
    const html = renderToStaticMarkup(<PushKeyPanel camera={camera()} onChanged={() => {}} client={client} initialAsk="rotate" />);
    expect(html).toContain("Rotate the key?");
    expect(html).toContain("set it up again on the iPhone");
    expect(html).toContain(">Rotate key<");
    expect(html).toContain(">Cancel<");
  });

  it("asks before revoking", () => {
    const html = renderToStaticMarkup(<PushKeyPanel camera={camera()} onChanged={() => {}} client={client} initialAsk="revoke" />);
    expect(html).toContain("Revoke the key?");
    expect(html).toContain(">Revoke key<");
  });

  it("says when the key is already revoked", () => {
    const html = renderToStaticMarkup(<PushKeyPanel camera={camera({ hasPushKey: false })} onChanged={() => {}} client={client} />);
    expect(html).toContain("Key revoked.");
    expect(html).not.toContain("Revoke key…");
  });

  it("rotates with POST and revokes with DELETE on push-key", async () => {
    calls.length = 0;
    expect(await runKeyAction(client, "c 1", "rotate")).toMatch(/^Key rotated\./);
    expect(await runKeyAction(client, "c 1", "revoke")).toMatch(/^Key revoked\./);
    expect(calls).toEqual([
      { method: "POST", url: "/api/v2/cameras/c%201/push-key" },
      { method: "DELETE", url: "/api/v2/cameras/c%201/push-key" },
    ]);
  });

  it("passes a refusal on to the dialog", async () => {
    await expect(runKeyAction(client, "denied", "rotate")).rejects.toThrow();
  });
});

describe("live tiles", () => {
  it("show a phone badge and Phone not streaming", () => {
    const html = renderToStaticMarkup(<VideoTile client={createClient()} camera={camera()} />);
    expect(html).toContain('aria-label="iPhone camera"');
    expect(html).toContain("Phone not streaming");
  });

  it("leave other cameras alone", () => {
    const html = renderToStaticMarkup(<VideoTile client={createClient()} camera={camera({ source: undefined, status: { online: false, viewers: 0, message: "No signal" } })} />);
    expect(html).not.toContain("iPhone camera");
    expect(html).toContain("No signal");
  });
});

describe("uploaded evidence", () => {
  const upload = {
    id: "e1",
    caseId: "EV-0042",
    cameraId: "",
    cameraName: "",
    title: "Broken gate",
    range: "",
    size: 200e6,
    sha256: "ab".repeat(32),
    lockedAt: 1_760_000_000,
    lockedBy: "ana",
    source: "upload",
    uploadedBy: "ana",
    device: "iPhone 16 Pro Max",
    recordedAt: at1402,
    fileName: "IMG_0042.MOV",
  } as SourcedEvidence;

  it("labels it Uploaded from iPhone with uploader, device and recordedAt", () => {
    expect(uploadedFromLabel("iPhone 16 Pro Max")).toBe("Uploaded from iPhone");
    expect(uploadedFromLabel(undefined)).toBe("Uploaded from iPhone");
    expect(uploadDetails(upload, () => "Oct 23 14:02")).toEqual(["by ana", "iPhone 16 Pro Max", "recorded Oct 23 14:02"]);
    expect(uploadDetails({ source: "upload" })).toEqual(["recording time unknown"]);
  });

  it("shows in the admin evidence list, playable and downloadable", () => {
    const html = renderToStaticMarkup(<EvidenceTable list={[upload]} checks={{}} onVerify={() => {}} onPlay={() => {}} />);
    expect(html).toContain("Broken gate");
    expect(html).toContain("Uploaded from iPhone");
    expect(html).toContain("by ana · iPhone 16 Pro Max · recorded");
    expect(html).toContain(">Play<");
    expect(html).toContain('href="/api/v2/evidence/e1/download"');
    expect(html).toContain('download="EV-0042.mov"');
  });

  it("shows in the Operator exports list", () => {
    const html = renderToStaticMarkup(<CaseSubject c={{ ...upload, note: "", fromMs: 0, toMs: 0, recorderId: "local" } as unknown as SourcedEvidenceCase} />);
    expect(html).toContain("Broken gate");
    expect(html).toContain("Uploaded from iPhone");
    expect(html).not.toContain("alarm");
  });

  it("keeps exported clips as they were", () => {
    const html = renderToStaticMarkup(
      <CaseSubject c={{ ...upload, source: undefined, cameraName: "Door", fromMs: 0, toMs: 0, range: "14:00–14:05", note: "", recorderId: "local" } as unknown as SourcedEvidenceCase} />,
    );
    expect(html).toContain("Door");
    expect(html).toContain("14:00–14:05");
    expect(html).not.toContain("Uploaded from");
    expect(evidenceFileName("EV-1")).toBe("EV-1.mp4");
  });
});

describe("server phone object (wave 23 server shape)", () => {
  it("reads hasPushKey, device and streaming from camera.phone", async () => {
    const { phoneStatus } = await import("../shared/phoneCameras");
    const base = { status: { online: false, message: "Phone not streaming" } } as never;
    expect(phoneStatus({ ...(base as object), phone: { streaming: false, hasPushKey: false } } as never).state).toBe("Key revoked");
    expect(phoneStatus({ ...(base as object), phone: { streaming: true, device: "EJ's iPhone", hasPushKey: true } } as never)).toMatchObject({ state: "Streaming", detail: "from EJ's iPhone" });
  });
});
