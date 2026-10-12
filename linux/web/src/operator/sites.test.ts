import { describe, expect, it } from "vitest";
import { normalizeAddress, parseSites, removeSite, sortSites, touchSite, upsertSite, type Site } from "./sites";

const addr = (s: string) => {
  const r = normalizeAddress(s);
  return "address" in r ? r.address : `error: ${r.error}`;
};

describe("normalizeAddress", () => {
  it("assumes a LAN server on http :8090 without a scheme", () => {
    expect(addr("ms.lan")).toBe("http://ms.lan:8090");
    expect(addr("  192.168.1.20 ")).toBe("http://192.168.1.20:8090");
    expect(addr("192.168.1.20:9000")).toBe("http://192.168.1.20:9000");
    expect(addr("[fd00::5]")).toBe("http://[fd00::5]:8090");
  });

  it("keeps an explicit scheme and its default port, and drops paths", () => {
    expect(addr("https://vms.example.com")).toBe("https://vms.example.com");
    expect(addr("https://vms.example.com:8443/admin/#/x")).toBe("https://vms.example.com:8443");
    expect(addr("HTTP://MS.LAN:8090/")).toBe("http://ms.lan:8090");
  });

  it("refuses what isn't a server address", () => {
    expect(addr("")).toMatch(/^error/);
    expect(addr("ftp://ms.lan")).toMatch(/^error/);
    expect(addr("https://ana:secret@ms.lan")).toMatch(/name and password/);
    expect(addr("http://")).toMatch(/^error/);
  });
});

describe("site list", () => {
  let n = 0;
  const id = () => `s${++n}`;

  it("adds, dedupes by address, renames and removes", () => {
    let { sites, site } = upsertSite([], { name: "", address: "ms.lan" }, id);
    expect(site).toEqual({ id: "s1", name: "ms.lan", address: "http://ms.lan:8090" });
    ({ sites } = upsertSite(sites, { name: "Warehouse", address: "http://ms.lan:8090/" }, id));
    expect(sites).toHaveLength(1);
    expect(sites[0].name).toBe("Warehouse");
    ({ sites } = upsertSite(sites, { name: "Office", address: "https://office.example.com" }, id));
    expect(sites.map((s) => s.id)).toEqual(["s1", "s2"]);
    // Editing one onto the other's address is refused.
    expect(() => upsertSite(sites, { id: "s2", name: "Office", address: "ms.lan" }, id)).toThrow(/Warehouse/);
    ({ sites } = upsertSite(sites, { id: "s2", name: "Front office", address: "https://office.example.com:8443" }, id));
    expect(sites[1]).toMatchObject({ id: "s2", name: "Front office", address: "https://office.example.com:8443" });
    expect(removeSite(sites, "s1").map((s) => s.id)).toEqual(["s2"]);
  });

  it("lists recently used sites first", () => {
    const sites: Site[] = [
      { id: "a", name: "Beta", address: "http://b:8090" },
      { id: "b", name: "Alpha", address: "http://a:8090" },
      { id: "c", name: "Gamma", address: "http://c:8090" },
    ];
    const touched = touchSite(sites, "c", "ana", 1000);
    expect(touched[2]).toMatchObject({ lastUser: "ana", lastUsedAt: 1000 });
    expect(sortSites(touched).map((s) => s.name)).toEqual(["Gamma", "Alpha", "Beta"]);
  });

  it("parses a stored list leniently", () => {
    const parsed = parseSites([{ id: "x", address: "ms.lan", name: "" }, { id: 3 }, null, { id: "y", address: "gopher://x" }, { id: "z", address: "https://h", lastUsedAt: "no" }]);
    expect(parsed).toEqual([
      { id: "x", name: "ms.lan", address: "http://ms.lan:8090", lastUser: undefined, lastUsedAt: undefined },
      { id: "z", name: "h", address: "https://h", lastUser: undefined, lastUsedAt: undefined },
    ]);
    expect(parseSites("nope")).toEqual([]);
  });
});
