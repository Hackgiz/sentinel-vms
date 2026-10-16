import { describe, expect, it } from "vitest";
import { groupPermissions } from "./RolesSection";

describe("groupPermissions", () => {
  it("groups by catalog group, keeping order", () => {
    const p = (id: string, group: string) => ({ id, label: id, group, description: "" });
    const g = groupPermissions([p("a", "One"), p("b", "Two"), p("c", "One")]);
    expect(g.map(([name, items]) => [name, items.map((i) => i.id)])).toEqual([
      ["One", ["a", "c"]],
      ["Two", ["b"]],
    ]);
  });
});
