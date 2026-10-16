// Building view requests that don't lose what the layout editor doesn't
// touch: a PUT replaces the whole view, so a save of a shared view must
// carry its shared flag, folder and order along or it would turn private.

import type { View, ViewInput } from "../shared/api";
import { viewFromLayout, type GridLayout } from "./layout";

/** The request that stores `view` again, with changes applied. */
export function viewInputOf(v: View, over: Partial<ViewInput> = {}): ViewInput {
  const out: ViewInput = { name: v.name, columns: v.columns, rows: v.rows, tiles: v.tiles, ...over };
  if (v.shared) {
    out.shared = true;
    if (v.groupId) out.groupId = v.groupId;
    if (v.sortOrder) out.sortOrder = v.sortOrder;
  }
  return { ...out, ...over };
}

/** Update `view` with a layout (keeps where a shared view sits). */
export function viewInputFromLayout(v: View, name: string, layout: GridLayout): ViewInput {
  return viewInputOf(v, viewFromLayout(name, layout));
}

/** A new view from a layout, private or shared in a folder. */
export function newViewInput(name: string, layout: GridLayout, where: { shared: boolean; groupId: string }): ViewInput {
  const base = viewFromLayout(name, layout);
  return where.shared ? { ...base, shared: true, ...(where.groupId ? { groupId: where.groupId } : {}) } : base;
}

/** Whether the viewer may change this view (shared ones need manage rights). */
export const canEditView = (v: View, canManage: boolean) => !v.shared || canManage;
