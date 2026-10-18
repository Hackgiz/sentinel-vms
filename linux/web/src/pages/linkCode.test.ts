import { describe, expect, it } from "vitest";
import { codeFromSearch, formatTyping, isCompleteCode, isLinkRoute } from "./linkCode";

describe("link page code helpers", () => {
  it("recognises the /link route", () => {
    expect(isLinkRoute({ pathname: "/link", hash: "" })).toBe(true);
    expect(isLinkRoute({ pathname: "/link/", hash: "" })).toBe(true);
    expect(isLinkRoute({ pathname: "/", hash: "#/link" })).toBe(true);
    expect(isLinkRoute({ pathname: "/", hash: "#/live" })).toBe(false);
    expect(isLinkRoute({ pathname: "/linked", hash: "" })).toBe(false);
  });

  it("formats what is typed", () => {
    expect(formatTyping("cdfg")).toBe("CDFG");
    expect(formatTyping("cdfgh")).toBe("CDFG-H");
    expect(formatTyping("cdfg hjkm")).toBe("CDFG-HJKM");
    expect(formatTyping("CDFG-HJKM-NP")).toBe("CDFG-HJKM");
    // Characters that can't be in a code are dropped.
    expect(formatTyping("b0c1d-oil8fg")).toBe("CDFG");
  });

  it("knows a whole code", () => {
    expect(isCompleteCode("CDFG-HJKM")).toBe(true);
    expect(isCompleteCode("CDFG-HJK")).toBe(false);
  });

  it("takes the code from the link", () => {
    expect(codeFromSearch("?code=cdfg-hjkm")).toBe("CDFG-HJKM");
    expect(codeFromSearch("")).toBe("");
  });
});
