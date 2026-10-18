// System tray icon: says at a glance whether the active site's management
// server answers. Full color while the server responds to /api/v1/status,
// dimmed grayscale while it doesn't (or no site is saved yet).
import { Menu, Tray, nativeImage, session, type NativeImage } from "electron";
import { join } from "node:path";

type Health = { state: "up" | "down" | "none"; detail: string };

export type TrayOptions = {
  partition: string;
  /** The site origin to watch (the focused window's, else the first saved). */
  currentOrigin: () => string | null;
  showOperator: () => void;
};

let tray: Tray | null = null;
let icons: { up: NativeImage; down: NativeImage } | null = null;
let health: Health = { state: "none", detail: "no site yet" };

function makeIcons(): { up: NativeImage; down: NativeImage } {
  const size = 16;
  const up = nativeImage.createFromPath(join(__dirname, "icon.png")).resize({ width: size, height: size });
  const bmp = Buffer.from(up.toBitmap()); // BGRA
  for (let i = 0; i < bmp.length; i += 4) {
    const gray = Math.round(0.11 * bmp[i] + 0.59 * bmp[i + 1] + 0.3 * bmp[i + 2]);
    bmp[i] = bmp[i + 1] = bmp[i + 2] = gray;
    bmp[i + 3] = Math.round(bmp[i + 3] * 0.55);
  }
  return { up, down: nativeImage.createFromBitmap(bmp, { width: size, height: size }) };
}

export function installTray(opts: TrayOptions) {
  if (tray) return;
  icons = makeIcons();
  tray = new Tray(icons.down);
  render(opts);

  const tick = async () => {
    const origin = opts.currentOrigin();
    if (!origin) {
      health = { state: "none", detail: "no site yet" };
    } else {
      const host = new URL(origin).host;
      try {
        const res = await session
          .fromPartition(opts.partition)
          .fetch(`${origin}/api/v1/status`, { signal: AbortSignal.timeout(5000), cache: "no-store" });
        health = res.ok
          ? { state: "up", detail: `server running (${host})` }
          : { state: "down", detail: `server error ${res.status} (${host})` };
      } catch {
        health = { state: "down", detail: `server unreachable (${host})` };
      }
    }
    render(opts);
  };
  void tick();
  setInterval(tick, 20_000).unref();
}

function render(opts: TrayOptions) {
  if (!tray || !icons) return;
  tray.setImage(health.state === "up" ? icons.up : icons.down);
  tray.setToolTip(`Sentinel — ${health.detail}`);
  tray.setContextMenu(
    Menu.buildFromTemplate([
      { label: `Sentinel: ${health.detail}`, enabled: false },
      { type: "separator" },
      { label: "Show Sentinel Operator", click: () => opts.showOperator() },
      { type: "separator" },
      { label: "Quit Sentinel Operator", role: "quit" },
    ]),
  );
}
