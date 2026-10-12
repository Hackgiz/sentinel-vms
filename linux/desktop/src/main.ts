// Sentinel Operator for Linux: the Electron main process.
//
// The UI is the operator page built from linux/web, bundled in the app and
// served from sentinel-app://operator/ (site.ts). Renderers run sandboxed
// with context isolation and no Node; everything native (saved sites,
// encrypted sign-ins, certificate pins, single sign-on, windows and
// displays) goes through the preload bridge (preload.ts) to the IPC
// handlers here.
import { app, BrowserWindow, ipcMain, Menu, protocol, screen, session, shell, type IpcMainInvokeEvent, type WebContents } from "electron";
import { join } from "node:path";
import { oidcCancel, oidcSignIn } from "./oidc";
import { appEntry, appOrigin, appScheme, bridgeCors, serveApp, verifyWithPins, type RefusedCertificate } from "./site";
import { JsonFile, Pins, TokenVault, type TokenPair } from "./storage";
import { displayFor, isPopoutTarget, placeWindow, popoutHash, setupKey, windowKey, type Layouts, type PopoutTarget, type WindowState } from "./windows";

protocol.registerSchemesAsPrivileged([{ scheme: appScheme, privileges: { standard: true, secure: true, supportFetchAPI: true, corsEnabled: true, stream: true } }]);

app.setName("Sentinel Operator");
// A separate profile (testing, or several operators on one login).
if (process.env.SENTINEL_OPERATOR_DATA) app.setPath("userData", process.env.SENTINEL_OPERATOR_DATA);
if (process.platform === "linux") app.commandLine.appendSwitch("enable-features", "VaapiVideoDecodeLinuxGL");

const partition = "persist:operator";
const rendererRoot = join(__dirname, "renderer");
const preload = join(__dirname, "preload.js");
const isDebug = !app.isPackaged || !!process.env.SENTINEL_DEBUG;

let dataDir = "";
let vault: TokenVault;
let pins: Pins;
let sitesFile: JsonFile<unknown[]>;
let layoutsFile: JsonFile<Layouts>;
let certs: { reset(): void };
const refused = new Map<string, RefusedCertificate>();
/** The site origin each window works with (webContents id → origin). */
const activeSite = new Map<number, string>();
const windows = new Map<string, BrowserWindow>();
let quitting = false;

// ---------------------------------------------------------------- windows ---

function currentLayout() {
  const displays = screen.getAllDisplays();
  const key = setupKey(displays);
  const all = layoutsFile.read();
  return { displays, key, layout: all[key] ?? { windows: {}, popouts: [] }, all };
}

function saveWindowState(key: string, win: BrowserWindow) {
  if (win.isDestroyed()) return;
  const { displays, key: setup, layout, all } = currentLayout();
  const bounds = win.isFullScreen() || win.isMaximized() ? win.getNormalBounds() : win.getBounds();
  const shown = win.isFullScreen() ? win.getBounds() : bounds;
  const state: WindowState = { bounds, displayId: displayFor(shown, displays)?.id ?? screen.getPrimaryDisplay().id, fullscreen: win.isFullScreen(), maximized: win.isMaximized() };
  layoutsFile.write({ ...all, [setup]: { ...layout, windows: { ...layout.windows, [key]: state } } });
}

function savePopouts() {
  const { key: setup, layout, all } = currentLayout();
  const popouts: PopoutTarget[] = [];
  for (const [, w] of windows) {
    const t = (w as BrowserWindow & { popout?: PopoutTarget }).popout;
    if (t && !w.isDestroyed()) popouts.push(t);
  }
  layoutsFile.write({ ...all, [setup]: { ...layout, popouts } });
}

function createWindow(target: PopoutTarget | null): BrowserWindow {
  const key = windowKey(target);
  const existing = windows.get(key);
  if (existing && !existing.isDestroyed()) {
    existing.show();
    existing.focus();
    return existing;
  }
  const { displays, layout } = currentLayout();
  const place = placeWindow(layout.windows[key], displays, screen.getPrimaryDisplay(), target ? { width: 960, height: 600 } : { width: 1440, height: 900 });
  const win = new BrowserWindow({
    ...place.bounds,
    minWidth: 480,
    minHeight: 320,
    show: false,
    backgroundColor: "#0a0e14",
    title: target?.title ? `${target.title} · Sentinel Operator` : "Sentinel Operator",
    autoHideMenuBar: !!target,
    webPreferences: {
      preload,
      partition,
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      webSecurity: true,
      spellcheck: false,
      additionalArguments: [`--sentinel-version=${app.getVersion()}`],
    },
  });
  (win as BrowserWindow & { popout?: PopoutTarget }).popout = target ?? undefined;
  windows.set(key, win);
  if (place.maximized) win.maximize();
  win.once("ready-to-show", () => {
    win.show();
    if (place.fullscreen) win.setFullScreen(true);
  });
  let saveTimer: ReturnType<typeof setTimeout> | null = null;
  const save = () => {
    if (saveTimer) clearTimeout(saveTimer);
    saveTimer = setTimeout(() => saveWindowState(key, win), 500);
  };
  for (const ev of ["move", "resize", "enter-full-screen", "leave-full-screen", "maximize", "unmaximize"] as const) win.on(ev as "move", save);
  win.on("close", () => {
    saveWindowState(key, win);
    // Closing the main window quits; its pop-outs are restored next time.
    if (!target && !quitting) {
      quitting = true;
      savePopouts();
      for (const [k, w] of windows) if (k !== key && !w.isDestroyed()) w.close();
    }
  });
  win.on("closed", () => {
    if (windows.get(key) === win) windows.delete(key);
    if (target && !quitting) savePopouts();
  });
  const id = win.webContents.id;
  win.webContents.on("destroyed", () => activeSite.delete(id));
  void win.loadURL(target ? `${appEntry}${popoutHash(target)}` : appEntry);
  if (target) savePopouts();
  return win;
}

// --------------------------------------------------------------- security ---

/** Every page: no navigation away from the app, no new windows, no webviews. */
function lockDown(wc: WebContents) {
  wc.on("will-navigate", (e, url) => {
    if (!url.startsWith(`${appOrigin}/`)) e.preventDefault();
  });
  wc.on("will-redirect", (e, url) => {
    if (!url.startsWith(`${appOrigin}/`)) e.preventDefault();
  });
  wc.on("will-attach-webview", (e) => e.preventDefault());
  wc.setWindowOpenHandler(({ url }) => {
    // Links to the signed-in site (e.g. Sentinel Admin) open in the system browser.
    const site = activeSite.get(wc.id);
    try {
      if (site && new URL(url).origin === site) void shell.openExternal(url);
    } catch {
      /* not a URL */
    }
    return { action: "deny" };
  });
}

function configureSession() {
  const ses = session.fromPartition(partition);
  serveApp(ses, rendererRoot);
  bridgeCors(ses);
  certs = verifyWithPins(ses, pins, refused);
  // Full screen for video is the only permission the app's pages ask for.
  ses.setPermissionRequestHandler((_wc, permission, cb) => cb(permission === "fullscreen"));
  ses.setPermissionCheckHandler((_wc, permission) => permission === "fullscreen");
  ses.on("will-download", (_e, item) => {
    item.setSaveDialogOptions({ title: "Save export", defaultPath: join(app.getPath("downloads"), item.getFilename()) });
  });
}

// -------------------------------------------------------------------- IPC ---

/** Only the app's own pages may call the bridge. */
function fromApp(e: IpcMainInvokeEvent): boolean {
  return (e.senderFrame?.url ?? "").startsWith(`${appOrigin}/`);
}

function handle<A extends unknown[], R>(channel: string, fn: (e: IpcMainInvokeEvent, ...args: A) => R) {
  ipcMain.handle(channel, (e, ...args) => {
    if (!fromApp(e)) throw new Error("not allowed");
    return fn(e, ...(args as A));
  });
}

const hostOf = (address: unknown) => {
  if (typeof address !== "string") throw new Error("bad address");
  return new URL(address).hostname.toLowerCase();
};

function isTokenPair(t: unknown): t is TokenPair {
  const o = t as TokenPair | null;
  return !!o && typeof o.accessToken === "string" && typeof o.refreshToken === "string" && typeof o.expiresAt === "number" && typeof o.refreshExpiresAt === "number";
}

function registerIpc() {
  handle("sites:list", () => sitesFile.read());
  handle("sites:save", (_e, list: unknown) => {
    if (!Array.isArray(list) || list.length > 500) throw new Error("bad site list");
    sitesFile.write(list);
  });

  handle("tokens:load", (_e, siteId: unknown) => (typeof siteId === "string" ? vault.load(siteId) : null));
  handle("tokens:save", (e, siteId: unknown, t: unknown) => {
    if (typeof siteId !== "string" || (t !== null && !isTokenPair(t))) throw new Error("bad tokens");
    vault.save(siteId, t);
    for (const w of BrowserWindow.getAllWindows()) if (!w.isDestroyed() && w.webContents.id !== e.sender.id) w.webContents.send("tokens:changed", siteId, t);
  });
  handle("tokens:encrypted", () => vault.encrypted());

  handle("site:set", (e, address: unknown) => {
    if (address === null) activeSite.delete(e.sender.id);
    else activeSite.set(e.sender.id, new URL(String(address)).origin);
  });

  handle("certs:pending", (_e, address: unknown) => refused.get(hostOf(address)) ?? null);
  handle("certs:trust", (_e, address: unknown, fingerprint: unknown) => {
    const host = hostOf(address);
    // Only a certificate this host actually presented can be pinned.
    const seen = refused.get(host);
    if (!seen || seen.fingerprint !== fingerprint) throw new Error("That certificate is no longer the one the site presents. Try again.");
    pins.set(host, seen.fingerprint);
    refused.delete(host);
    certs.reset();
  });
  handle("certs:forget", (_e, address: unknown) => {
    pins.set(hostOf(address), null);
    certs.reset();
  });
  handle("certs:pinned", (_e, address: unknown) => pins.get(hostOf(address)));

  handle("oidc:signIn", (e, address: unknown) => {
    const origin = new URL(String(address)).origin;
    if (activeSite.get(e.sender.id) !== origin) throw new Error("Choose the site first.");
    return oidcSignIn(origin);
  });
  handle("oidc:cancel", () => oidcCancel());

  handle("windows:open", (_e, target: unknown) => {
    if (!isPopoutTarget(target)) throw new Error("bad window target");
    createWindow({ ...target, title: typeof target.title === "string" ? target.title.slice(0, 100) : undefined });
  });
  handle("windows:displays", () => {
    const primary = screen.getPrimaryDisplay().id;
    return screen.getAllDisplays().map((d, i) => ({ id: d.id, label: d.label || `Display ${i + 1}`, primary: d.id === primary, bounds: d.bounds }));
  });
  handle("windows:fullscreen", (e, displayId: unknown) => {
    const win = BrowserWindow.fromWebContents(e.sender);
    if (!win) return;
    if (displayId === null) {
      win.setFullScreen(false);
      return;
    }
    const d = screen.getAllDisplays().find((x) => x.id === displayId);
    if (!d) throw new Error("That display isn't connected.");
    const go = () => {
      win.setBounds({ ...d.workArea, width: Math.min(d.workArea.width, 1280), height: Math.min(d.workArea.height, 800) });
      win.setFullScreen(true);
    };
    if (win.isFullScreen()) {
      win.once("leave-full-screen", go);
      win.setFullScreen(false);
    } else go();
  });
}

// ------------------------------------------------------------------- menu ---

function buildMenu() {
  const template: Electron.MenuItemConstructorOptions[] = [
    { label: "File", submenu: [{ role: "quit" }] },
    { label: "Edit", submenu: [{ role: "undo" }, { role: "redo" }, { type: "separator" }, { role: "cut" }, { role: "copy" }, { role: "paste" }, { role: "selectAll" }] },
    {
      label: "View",
      submenu: [
        { role: "reload" },
        ...(isDebug ? ([{ role: "toggleDevTools" }] as Electron.MenuItemConstructorOptions[]) : []),
        { type: "separator" },
        { role: "resetZoom" },
        { role: "zoomIn" },
        { role: "zoomOut" },
        { type: "separator" },
        { role: "togglefullscreen" },
      ],
    },
    { label: "Window", submenu: [{ role: "minimize" }, { role: "close" }] },
  ];
  Menu.setApplicationMenu(Menu.buildFromTemplate(template));
}

// ------------------------------------------------------------------ start ---

if (!app.requestSingleInstanceLock()) {
  app.quit();
} else {
  app.on("second-instance", () => {
    const main = windows.get("main");
    if (main && !main.isDestroyed()) {
      if (main.isMinimized()) main.restore();
      main.focus();
    }
  });
  app.on("web-contents-created", (_e, wc) => lockDown(wc));
  app.on("before-quit", () => {
    if (!quitting) {
      quitting = true;
      savePopouts();
    }
  });
  app.on("window-all-closed", () => app.quit());

  void app.whenReady().then(() => {
    dataDir = app.getPath("userData");
    vault = new TokenVault(dataDir);
    pins = new Pins(dataDir);
    sitesFile = new JsonFile(join(dataDir, "sites.json"), () => []);
    layoutsFile = new JsonFile(join(dataDir, "windows.json"), () => ({}));
    configureSession();
    registerIpc();
    buildMenu();
    createWindow(null);
    // Reopen this display setup's pop-outs.
    for (const t of currentLayout().layout.popouts) if (isPopoutTarget(t)) createWindow(t);
  });
}
