// The Sentinel Cloud card rendered from its state, without a browser.
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { CloudStatusWithSignIn } from "../admin/cloud";
import { cardReducer, initialCardState, parseSignIn, type CardEvent, type CardState } from "../cloudAccount";
import { CardView, type CardHandlers, type CardViewProps } from "./CloudPage";

const signIn = {
  supabaseUrl: "https://bfmpsvpnxjjrbvbcelqd.supabase.co",
  supabaseAnonKey: "sb_publishable_test",
  siteBase: "https://sentvms.com",
  checkoutURL: "https://bfmpsvpnxjjrbvbcelqd.supabase.co/functions/v1/sentinel-create-checkout",
  portalURL: "https://bfmpsvpnxjjrbvbcelqd.supabase.co/functions/v1/sentinel-portal",
  plans: [
    { id: "cloud", name: "Sentinel Cloud", month: 4.99, year: 49, servers: 1, backupGB: 10, days: 30 },
    { id: "business", name: "Sentinel Cloud Business", month: 19.99, year: 199, servers: 5, backupGB: 100, days: 90 },
  ],
};

function status(over: Partial<CloudStatusWithSignIn> = {}): CloudStatusWithSignIn {
  return {
    linked: true,
    serverId: "abc123",
    serverName: "Home",
    accountEmailMasked: "e***@example.com",
    plan: "cloud",
    status: "active",
    periodEnd: null,
    features: ["relay", "push", "backup"],
    quotas: { backupBytes: 10 * 1024 ** 3, backupDays: 30, servers: 1 },
    remote: { enabled: true, state: "connected", url: "https://abc123-cloud.sentvms.com", error: "" },
    push: { state: "cloud" },
    backup: { enabled: true, bytesUsed: 1.2 * 1024 ** 3, objects: 3, lastUploadAt: null, error: "" },
    signIn,
    ...over,
  };
}

const unlinked = status({ linked: false, plan: "free", status: "inactive", accountEmailMasked: "", serverId: "", features: [], remote: { enabled: false, state: "off", url: "", error: "" }, push: { state: "off" }, backup: { enabled: false, bytesUsed: 0, objects: 0, lastUploadAt: null, error: "" } });

const noop = () => {};
const on: CardHandlers = { email: noop, password: noop, mode: noop, plan: noop, billing: noop, turnOn: noop, act: noop, signInAndContinue: noop, cancelSignIn: noop, turnOff: noop, retry: noop, put: noop, cancelCode: noop };

const reach = (events: CardEvent[]): CardState => events.reduce(cardReducer, initialCardState);

function render(over: Partial<CardViewProps>): string {
  const s = over.status === undefined ? null : over.status;
  const props: CardViewProps = {
    state: initialCardState,
    status: s,
    address: null,
    config: parseSignIn(s?.signIn),
    canEdit: true,
    email: "",
    password: "",
    plan: "",
    billing: "month",
    on,
    ...over,
  };
  return renderToStaticMarkup(<CardView {...props} />);
}

const notLinked = reach([{ type: "loaded", linked: false }]);
const linked = reach([{ type: "loaded", linked: true }]);

describe("CardView", () => {
  it("loads", () => {
    expect(render({})).toContain("Loading");
  });

  it("asks for email and password while not linked", () => {
    const html = render({ state: notLinked, status: unlinked });
    expect(html).toContain('type="email"');
    expect(html).toContain('type="password"');
    expect(html).toContain('autoComplete="current-password"');
    expect(html).toContain("Turn on Sentinel Cloud");
    expect(html).toContain("Create account");
    expect(html).not.toContain("Subscribe");
  });

  it("switches to Create account", () => {
    const html = render({ state: cardReducer(notLinked, { type: "mode", mode: "signUp" }), status: unlinked });
    expect(html).toContain('autoComplete="new-password"');
    expect(html).toContain("At least 6 characters.");
  });

  it("asks to confirm the email after a sign-up", () => {
    const html = render({ state: reach([{ type: "loaded", linked: false }, { type: "confirmNeeded", email: "ana@example.com" }]), status: unlinked });
    expect(html).toContain("ana@example.com");
    expect(html).toContain("I confirmed it, sign me in");
  });

  it("disables the form while linking", () => {
    const html = render({ state: reach([{ type: "loaded", linked: false }, { type: "submit" }, { type: "authed" }]), status: unlinked });
    expect(html).toContain("Turning on…");
    expect(html).toMatch(/<input type="email"[^>]*disabled=""/);
  });

  it("shows the error under the button, with the upgrade link", () => {
    const html = render({ state: reach([{ type: "loaded", linked: false }, { type: "submit" }, { type: "linkFailed", message: "No room.", upgradeURL: "https://sentvms.com/cloud" }]), status: unlinked });
    expect(html).toContain('role="alert"');
    expect(html).toContain("No room.");
    expect(html).toContain('href="https://sentvms.com/cloud"');
  });

  it("says when Sentinel Cloud can't be reached for sign-in", () => {
    const html = render({ state: notLinked, status: { ...unlinked, signIn: null } });
    expect(html).toContain("can&#x27;t reach Sentinel Cloud");
    expect(html).not.toContain('type="password"');
  });

  it("shows a viewer no form while not linked", () => {
    const html = render({ state: notLinked, status: unlinked, canEdit: false });
    expect(html).not.toContain("<input");
    expect(html).not.toContain("<button");
    expect(html).toContain("An administrator can turn it on");
  });

  it("shows the three lines and Manage plan on an active plan", () => {
    const html = render({ state: linked, status: status() });
    expect(html).toContain("Remote address");
    expect(html).toContain("connected");
    expect(html).toContain("https://abc123-cloud.sentvms.com");
    expect(html).toContain("Copy");
    expect(html).toContain("through Sentinel Cloud");
    expect(html).toContain("on · 1.2 GB of 10 GB");
    expect(html).toContain("Manage plan");
    expect(html).toContain("Turn off");
    expect(html).toContain("Sentinel Cloud · Active");
    expect(html).not.toContain("Subscribe");
  });

  it("offers Subscribe with the plans on a free server", () => {
    const html = render({ state: linked, status: status({ plan: "free", status: "inactive", features: [], remote: { enabled: true, state: "inactive", url: "", error: "" }, push: { state: "off" } }) });
    expect(html).toContain("Subscribe");
    expect(html).toContain("Sentinel Cloud · $4.99/mo · $49/yr");
    expect(html).toContain("Sentinel Cloud Business · $19.99/mo · $199/yr");
    expect(html).toContain("Monthly");
    expect(html).toContain("Yearly");
    expect(html).toContain("1 server · 10 GB offsite backup · 30 days of alarms · $4.99/mo");
    expect(html).not.toContain("Manage plan");
    expect(html).toContain("Free");
  });

  it("prices the chosen plan and interval", () => {
    const html = render({ state: linked, status: status({ plan: "free", status: "inactive" }), plan: "business", billing: "year" });
    expect(html).toContain("5 servers · 100 GB offsite backup · 90 days of alarms · $199/yr");
  });

  it("offers Subscribe again after a cancellation", () => {
    expect(render({ state: linked, status: status({ status: "canceled" }) })).toContain("Subscribe again");
  });

  it("asks for the password inline when the JWT is gone", () => {
    const state = cardReducer(linked, { type: "needSignIn", action: { kind: "portal" } });
    const html = render({ state, status: status() });
    expect(html).toContain("Sign in to Sentinel Cloud as e***@example.com to manage the plan.");
    expect(html).toContain("Sign in and continue");
    expect(html).toContain('type="password"');
  });

  it("shows a viewer the status lines only", () => {
    const html = render({ state: linked, status: status(), canEdit: false });
    expect(html).toContain("Remote address");
    expect(html).toContain("through Sentinel Cloud");
    for (const word of ["Subscribe", "Manage plan", "Turn off", "<input", "<select"]) expect(html).not.toContain(word);
  });

  it("shows someone without the full status the remote line", () => {
    const viewer = reach([{ type: "viewer" }]);
    expect(render({ state: viewer, address: { remoteURL: "https://abc123-cloud.sentvms.com", via: "cloud" } })).toContain("https://abc123-cloud.sentvms.com");
    const off = render({ state: viewer, address: null });
    expect(off).toContain("off");
    expect(off).not.toContain("<input");
  });

  it("waits for the plan after Stripe Checkout", () => {
    const state = cardReducer(linked, { type: "checkoutReturn" });
    expect(render({ state, status: status({ plan: "free", status: "inactive" }) })).toContain("Waiting for Sentinel Cloud to confirm your subscription");
    expect(render({ state: cardReducer(state, { type: "checkoutDone" }), status: status() })).toContain("Sentinel Cloud is active on this server");
    expect(render({ state: cardReducer(state, { type: "checkoutTimeout" }), status: status({ plan: "free", status: "inactive" }) })).toContain("within a few minutes");
  });

  it("offers a retry when the status can't load", () => {
    const html = render({ state: reach([{ type: "loadFailed", message: "Down." }]) });
    expect(html).toContain("Down.");
    expect(html).toContain("Try again");
  });

  it("never says tunnel", () => {
    const views = [
      render({ state: notLinked, status: unlinked }),
      render({ state: notLinked, status: { ...unlinked, signIn: null } }),
      render({ state: linked, status: status() }),
      render({ state: linked, status: status({ plan: "free", status: "inactive" }) }),
      render({ state: linked, status: status({ remote: { enabled: true, state: "retrying", url: "", error: "relay dropped" } }) }),
      render({ state: reach([{ type: "viewer" }]), address: { remoteURL: "https://x.trycloudflare.com", via: "tunnel" } }),
    ];
    for (const html of views) expect(html.toLowerCase()).not.toContain("tunnel");
  });
});
