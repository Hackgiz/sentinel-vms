// The bridge the operator page sees as `window.sentinelDesktop`
// (DesktopBridge in linux/web/src/operator/bridge.ts). Runs sandboxed:
// only `electron`'s renderer modules, no Node. Exposes functions, never
// ipcRenderer itself.
import { contextBridge, ipcRenderer } from "electron";
import type { DesktopBridge } from "../../web/src/operator/bridge";

const versionArg = process.argv.find((a) => a.startsWith("--sentinel-version="));

const bridge: DesktopBridge = {
  version: versionArg ? versionArg.slice("--sentinel-version=".length) : "",
  platform: process.platform,
  sites: {
    list: () => ipcRenderer.invoke("sites:list"),
    save: (sites) => ipcRenderer.invoke("sites:save", sites),
  },
  tokens: {
    load: (siteId) => ipcRenderer.invoke("tokens:load", siteId),
    save: (siteId, tokens) => ipcRenderer.invoke("tokens:save", siteId, tokens),
    onChange: (fn) => {
      const listener = (_e: Electron.IpcRendererEvent, siteId: string, tokens: Parameters<typeof fn>[1]) => fn(siteId, tokens);
      ipcRenderer.on("tokens:changed", listener);
      return () => {
        ipcRenderer.removeListener("tokens:changed", listener);
      };
    },
    encrypted: () => ipcRenderer.invoke("tokens:encrypted"),
  },
  setSite: (address) => ipcRenderer.invoke("site:set", address),
  certificates: {
    pending: (address) => ipcRenderer.invoke("certs:pending", address),
    trust: (address, fingerprint) => ipcRenderer.invoke("certs:trust", address, fingerprint),
    forget: (address) => ipcRenderer.invoke("certs:forget", address),
    pinned: (address) => ipcRenderer.invoke("certs:pinned", address),
  },
  oidcSignIn: (address) => ipcRenderer.invoke("oidc:signIn", address),
  oidcCancel: () => ipcRenderer.invoke("oidc:cancel"),
  windows: {
    open: (target) => ipcRenderer.invoke("windows:open", target),
    displays: () => ipcRenderer.invoke("windows:displays"),
    fullscreen: (displayId) => ipcRenderer.invoke("windows:fullscreen", displayId),
  },
  notifications: {
    alarm: (notice) => ipcRenderer.invoke("notify:alarm", notice),
    onOpenAlarm: (fn) => {
      const listener = (_e: Electron.IpcRendererEvent, alarmId: string) => fn(alarmId);
      ipcRenderer.on("notify:open-alarm", listener);
      return () => {
        ipcRenderer.removeListener("notify:open-alarm", listener);
      };
    },
  },
};

contextBridge.exposeInMainWorld("sentinelDesktop", bridge);
