// "Recover from camera" on the Playback timeline (Wave 9, stream C): one
// edge recovery at a time. Its progress is read with one timer that runs
// only while a job is running and the page is visible (the long-export
// precedent); when footage came back, onRecovered re-reads the timeline.
import { useCallback, useEffect, useRef, useState } from "react";
import { errorMessage, type ApiClient, type EdgeRecovery } from "../shared/api";
import { usePageVisible } from "./CameraGrid";
import { edgeJobActive, edgeJobText } from "./edgeRecovery";
import type { Range } from "./timelineMath";
import { notify } from "./notices";

export const edgePollMs = 2000;

export function useEdgeRecovery(client: ApiClient, onRecovered: () => void) {
  const visible = usePageVisible();
  const [job, setJob] = useState<EdgeRecovery | null>(null);
  const [error, setError] = useState("");
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  const recovered = useRef(onRecovered);
  recovered.current = onRecovered;

  const finished = useCallback((j: EdgeRecovery) => {
    notify(j.state === "failed" ? "error" : j.state === "done" && j.recovered ? "success" : "info", edgeJobText(j));
    if (j.state === "done" || j.state === "canceled") {
      if (j.recovered) recovered.current();
    }
  }, []);

  const running = edgeJobActive(job);
  const taskId = job?.taskId;
  useEffect(() => {
    if (!running || !visible || !taskId) return;
    const t = setInterval(
      () =>
        void client.edgeRecovery
          .get(taskId)
          .then((j) => {
            if (!alive.current) return;
            setJob(j);
            if (!edgeJobActive(j)) finished(j);
          })
          .catch((e) => alive.current && setError(errorMessage(e))),
      edgePollMs,
    );
    return () => clearInterval(t);
  }, [running, visible, taskId, client, finished]);

  const start = useCallback(
    async (cameraId: string, r: Range) => {
      setError("");
      try {
        const j = await client.edgeRecovery.start(cameraId, Math.round(r.start), Math.round(r.end));
        if (!alive.current) return;
        setJob(j);
        if (!edgeJobActive(j)) finished(j);
      } catch (e) {
        if (alive.current) setError(errorMessage(e));
      }
    },
    [client, finished],
  );

  const cancel = useCallback(async () => {
    if (!taskId) return;
    try {
      const j = await client.edgeRecovery.cancel(taskId);
      if (!alive.current) return;
      setJob(j);
      finished(j);
    } catch (e) {
      if (alive.current) setError(errorMessage(e));
    }
  }, [client, taskId, finished]);

  return { job, running, message: error || (job ? edgeJobText(job) : ""), start, cancel };
}
