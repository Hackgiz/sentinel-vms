// Wave 14's rule calls: POST /api/v2/rules/draft and GET /api/v2/rule-templates.
// fetch is mocked; nothing leaves the process.
import { afterEach, describe, expect, it, vi } from "vitest";
import { draftRule, groupTemplates, listRuleTemplates, type RuleTemplate } from "./rulesApi";
import { MemoryTokenStore, createClient, type RuleInput } from "./shared/api";

const rule: RuleInput = {
  name: "After-hours people",
  enabled: false,
  cameraIds: [],
  kinds: ["Person"],
  minSeverity: "Warning",
  schedule: true,
  fromMinute: 18 * 60,
  toMinute: 7 * 60,
  days: [0, 1, 2, 3, 4, 5, 6],
  delivery: "push",
  instructions: "Open the live view and check the person.",
};

function respond(code: number, body: unknown) {
  return vi.fn(async () => new Response(JSON.stringify(body), { status: code, headers: { "Content-Type": "application/json" } }));
}

afterEach(() => vi.unstubAllGlobals());

describe("draftRule", () => {
  it("posts the trimmed text and returns the draft", async () => {
    const draft = { rule, explanation: "Alarms for people after 6pm on every camera.", warnings: ['No camera called "Loading dock".'] };
    const f = respond(200, draft);
    vi.stubGlobal("fetch", f);
    expect(await draftRule("  people at the loading dock after 6pm \n")).toEqual(draft);
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v2/rules/draft");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ text: "people at the loading dock after 6pm" });
  });

  it("carries the server's reason when AI is off", async () => {
    vi.stubGlobal("fetch", respond(409, { error: { code: "conflict", message: "AI is off" } }));
    await expect(draftRule("people after 6pm")).rejects.toMatchObject({ status: 409, message: "AI is off" });
  });
});

describe("rule templates", () => {
  const t = (id: string, category: string): RuleTemplate => ({ id, category: category as RuleTemplate["category"], title: id, summary: "", rule });

  it("lists the catalog", async () => {
    const items = [t("tamper", "security")];
    const f = respond(200, { items });
    vi.stubGlobal("fetch", f);
    expect(await listRuleTemplates()).toEqual(items);
    expect((f.mock.calls[0] as unknown as [string])[0]).toBe("/api/v2/rule-templates");
  });

  it("groups by category in catalog order, unknown categories last", () => {
    const groups = groupTemplates([t("loud-noise", "operations"), t("storage-low", "health"), t("tamper", "security"), t("x", "facilities"), t("loitering-entrance", "security")]);
    expect(groups.map((g) => [g.label, g.items.map((i) => i.id)])).toEqual([
      ["Security", ["tamper", "loitering-entrance"]],
      ["Health", ["storage-low"]],
      ["Operations", ["loud-noise"]],
      ["Facilities", ["x"]],
    ]);
    expect(groupTemplates([])).toEqual([]);
  });
});

// WAVE15: the Operator passes its bearer-token client; the default stays the Admin's same-origin cookie client.
describe("an injected client", () => {
  const now = 1_700_000_000_000;
  const operator = (fetch: typeof globalThis.fetch) =>
    createClient({
      baseUrl: "https://ms.lan:8090/",
      tokens: new MemoryTokenStore({ accessToken: "op-token", expiresAt: now + 3600_000, refreshToken: "r", refreshExpiresAt: now + 86400_000 }),
      fetch,
      now: () => now,
    });
  const bearer = (init: RequestInit) => new Headers(init.headers).get("Authorization");

  it("drafts a rule on the operator's server with its token", async () => {
    const f = respond(200, { rule, explanation: "", warnings: [] });
    vi.stubGlobal("fetch", vi.fn());
    await draftRule("people after 6pm", operator(f as unknown as typeof fetch));
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("https://ms.lan:8090/api/v2/rules/draft");
    expect(bearer(init)).toBe("Bearer op-token");
    expect(globalThis.fetch).not.toHaveBeenCalled();
  });

  it("lists templates on the operator's server with its token", async () => {
    const f = respond(200, { items: [] });
    expect(await listRuleTemplates(operator(f as unknown as typeof fetch))).toEqual([]);
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("https://ms.lan:8090/api/v2/rule-templates");
    expect(bearer(init)).toBe("Bearer op-token");
  });

  it("leaves the default on the Admin's relative URL, without a bearer token", async () => {
    const f = respond(200, { items: [] });
    vi.stubGlobal("fetch", f);
    await listRuleTemplates();
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v2/rule-templates");
    expect(bearer(init ?? {})).toBeNull();
  });
});
