// WAVE17: how the People and Plates sections run outside the Admin. The
// Admin uses its cookie client and /api/v2/me; the Operator passes its
// bearer client and its own permission check. Images follow the client:
// <img src> works only with the cookie, so a bearer client fetches the bytes.
import { createContext } from "react";
import { detectionApi, type DetectionApi } from "../detectionApi";
import type { ApiClient, Permission } from "../shared/api";
import { v2 } from "./client";

export interface SectionHost {
  /** The API client; the Operator passes its bearer-token client. Default: the Admin's. */
  client?: ApiClient;
  /** The host's permission check, so the section needn't load /api/v2/me itself. */
  can?: (p: Permission) => boolean;
}

export interface DetectionCtx {
  client: ApiClient;
  api: DetectionApi;
  /** True for the Admin's cookie client: images load by <img src>. */
  direct: boolean;
}

export function detectionCtx(client: ApiClient = v2): DetectionCtx {
  return { client, api: detectionApi(client), direct: client === v2 };
}

export const DetectionContext = createContext<DetectionCtx>(detectionCtx());

/** An event snapshot's path (see shared/AuthImage). */
export const thumbnailPath = (eventId: string) => `/api/v2/events/${encodeURIComponent(eventId)}/thumbnail.jpg`;
