// WAVE20: line icons for the operator rail, cards and chips, standing in for
// the SF Symbols the Mac and iPhone apps use. 24-unit grid, drawn in
// currentColor, so a parent's colour tints them.
import type { ReactNode } from "react";

const paths: Record<string, ReactNode> = {
  home: (
    <>
      <path d="M3 10.5 12 3l9 7.5" />
      <path d="M5 9.5V20a1 1 0 0 0 1 1h4v-6h4v6h4a1 1 0 0 0 1-1V9.5" />
    </>
  ),
  video: (
    <>
      <rect x="2" y="6" width="14" height="12" rx="2.5" />
      <path d="m16 10.5 6-3.5v10l-6-3.5" />
    </>
  ),
  videoOff: (
    <>
      <path d="M10 6h3.5A2.5 2.5 0 0 1 16 8.5v3.5l6-3.5v10l-3-1.75" />
      <path d="M16 16v-.5M4.5 6A2.5 2.5 0 0 0 2 8.5v7A2.5 2.5 0 0 0 4.5 18h9a2.5 2.5 0 0 0 1.8-.8" />
      <path d="m3 3 18 18" />
    </>
  ),
  map: (
    <>
      <path d="M9 4 3 6.5V20l6-2.5 6 2.5 6-2.5V4l-6 2.5z" />
      <path d="M9 4v13.5M15 6.5V20" />
    </>
  ),
  search: (
    <>
      <circle cx="11" cy="11" r="7" />
      <path d="m20 20-4-4" />
    </>
  ),
  export: (
    <>
      <path d="M12 3v12M7 8l5-5 5 5" />
      <path d="M5 14v5a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-5" />
    </>
  ),
  bell: (
    <>
      <path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9" />
      <path d="M10.3 21a1.94 1.94 0 0 0 3.4 0" />
    </>
  ),
  incident: (
    <>
      <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
      <path d="M12 10.5V14M12 17h.01" />
    </>
  ),
  monitor: <path d="M22 12h-4l-3 8L9 4l-3 8H2" />,
  reports: (
    <>
      <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z" />
      <path d="M14 3v5h5M9 13h6M9 17h6" />
    </>
  ),
  insights: <path d="M4 20h16M7 16v-5M12 16V6M17 16V9" />,
  rules: (
    <>
      <path d="M4 6h9M19 6h1M4 12h3M13 12h7M4 18h11M21 18h-1" />
      <circle cx="16" cy="6" r="2.5" />
      <circle cx="10" cy="12" r="2.5" />
      <circle cx="18" cy="18" r="2.5" />
    </>
  ),
  people: (
    <>
      <circle cx="9" cy="8" r="3.5" />
      <path d="M2.5 20a6.5 6.5 0 0 1 13 0" />
      <path d="M16 4.6a3.5 3.5 0 0 1 0 6.8M18 14a6.5 6.5 0 0 1 3.5 6" />
    </>
  ),
  plates: (
    <>
      <rect x="2" y="7" width="20" height="10" rx="2" />
      <path d="M6 12h.01M9 12h6M18 12h.01" />
    </>
  ),
  settings: (
    <>
      <path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z" />
      <circle cx="12" cy="12" r="3" />
    </>
  ),
  shield: (
    <>
      <path d="M12 21.5s8-3.8 8-9.8V5.2L12 2.5 4 5.2v6.5c0 6 8 9.8 8 9.8z" />
      <path d="M12 2.5v19c-.1 0-8-3.8-8-9.8V5.2z" fill="currentColor" stroke="none" />
    </>
  ),
  walk: (
    <>
      <circle cx="13.5" cy="4" r="2" />
      <path d="m9 21 2.5-6.5L14 17v4M8 12.5l1.5-4.5 4-.5 2 3.5 3 1M11.5 14.5l1.5-6" />
    </>
  ),
  car: (
    <>
      <path d="M3 16.5v-4.5l2.4-5.2A1.5 1.5 0 0 1 6.8 6h10.4a1.5 1.5 0 0 1 1.4.8L21 12v4.5z" />
      <path d="M3 12h18M6.5 14.5h.01M17.5 14.5h.01M5 16.5V19M19 16.5V19" />
    </>
  ),
  plate: (
    <>
      <rect x="2" y="7" width="20" height="10" rx="2" />
      <path d="M6 12h.01M9 12h6M18 12h.01" />
    </>
  ),
  clock: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7v5l3 2" />
    </>
  ),
  flag: <path d="M5 21V4M5 4h12l-2.5 4.5L17 13H5" />,
  paw: (
    <>
      <circle cx="6" cy="10" r="1.8" />
      <circle cx="10" cy="6" r="1.8" />
      <circle cx="14" cy="6" r="1.8" />
      <circle cx="18" cy="10" r="1.8" />
      <path d="M8 17.5c0-2.5 1.8-5 4-5s4 2.5 4 5c0 1.5-1.2 2.5-2.5 2.5h-3C9.2 20 8 19 8 17.5z" />
    </>
  ),
  face: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="M9 10h.01M15 10h.01M8.5 15a4 4 0 0 0 7 0" />
    </>
  ),
  sparkles: (
    <>
      <path d="M11 3l1.8 5.2L18 10l-5.2 1.8L11 17l-1.8-5.2L4 10l5.2-1.8z" />
      <path d="M19 14.5l.7 1.8 1.8.7-1.8.7-.7 1.8-.7-1.8-1.8-.7 1.8-.7z" />
    </>
  ),
  record: (
    <>
      <circle cx="12" cy="12" r="9" />
      <circle cx="12" cy="12" r="4" fill="currentColor" />
    </>
  ),
  drive: (
    <>
      <rect x="2" y="13" width="20" height="7" rx="2" />
      <path d="M4.5 13 7 5.5h10l2.5 7.5M6 16.5h.01M10 16.5h.01" />
    </>
  ),
  arrows: <path d="M3 12h18M7 8l-4 4 4 4M17 8l4 4-4 4" />,
  key: (
    <>
      <circle cx="7.5" cy="15.5" r="4.5" />
      <path d="m10.7 12.3 9.3-9.3M17 6l3 3M14 9l2 2" />
    </>
  ),
  alert: (
    <>
      <path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z" />
      <path d="M12 9v4M12 17h.01" />
    </>
  ),
  octagon: (
    <>
      <path d="M7.9 2h8.2L22 7.9v8.2L16.1 22H7.9L2 16.1V7.9z" />
      <path d="M12 8v4M12 16h.01" />
    </>
  ),
  motion: <path d="M2 12c2-4 4-4 6 0s4 4 6 0 4-4 6 0M2 18c2-4 4-4 6 0M14 6c2-4 4-4 6 0" />,
  sound: (
    <>
      <path d="M11 5 6 9H2v6h4l5 4z" />
      <path d="M15.5 8.5a5 5 0 0 1 0 7M19 5a10 10 0 0 1 0 14" />
    </>
  ),
  // PARITY-342 §2: Package (shippingbox), Intrusion (figure.walk.departure), audio/Sound (waveform).
  package: (
    <>
      <path d="M21 8 12 3 3 8v8l9 5 9-5z" />
      <path d="m3 8 9 5 9-5M12 13v8M7.5 5.5l9 5" />
    </>
  ),
  intrusion: (
    <>
      <path d="M14 3h6v18h-6" />
      <circle cx="8.5" cy="5" r="1.8" />
      <path d="m5 21 2-5.5 2.5 2V21M4 12l1.5-3.5 3.5-.5 1.5 3 2.5.5M7 15.5l1.5-5.5" />
    </>
  ),
  waveform: <path d="M3 10v4M7 7v10M11 4v16M15 8v8M19 6v12M23 11v2" />,
  check: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="m8 12 3 3 5-6" />
    </>
  ),
  shieldCheck: (
    <>
      <path d="M12 21.5s8-3.8 8-9.8V5.2L12 2.5 4 5.2v6.5c0 6 8 9.8 8 9.8z" />
      <path d="m8.5 12 2.5 2.5 4.5-5" />
    </>
  ),
  play: <path d="M7 4v16l13-8z" fill="currentColor" />,
  chevron: <path d="m9 6 6 6-6 6" />,
  pin: (
    <>
      <path d="M12 22s7-6.2 7-12a7 7 0 0 0-14 0c0 5.8 7 12 7 12z" />
      <circle cx="12" cy="10" r="2.5" />
    </>
  ),
  expand: <path d="M15 3h6v6M9 21H3v-6M21 3l-7 7M3 21l7-7" />,
  shrink: <path d="M4 14h6v6M20 10h-6V4M14 10l7-7M3 21l7-7" />,
  open: <path d="M14 4h6v6M20 4l-9 9M18 14v4a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4" />,
  // WAVE21: the server dashboard's rail.
  phone: (
    <>
      <rect x="6" y="2" width="12" height="20" rx="3" />
      <path d="M11 18h2" />
    </>
  ),
  cloud: <path d="M7 18h10.5a4.5 4.5 0 0 0 .6-8.96A6 6 0 0 0 6.6 8.1 5 5 0 0 0 7 18z" />,
  list: <path d="M9 6h11M9 12h11M9 18h11M4 6h.01M4 12h.01M4 18h.01" />,
  user: (
    <>
      <circle cx="12" cy="8" r="4" />
      <path d="M4 21a8 8 0 0 1 16 0" />
    </>
  ),
  mail: (
    <>
      <rect x="3" y="5" width="18" height="14" rx="2.5" />
      <path d="m3.5 6.5 8.5 6.5 8.5-6.5" />
    </>
  ),
  help: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="M9.5 9.5a2.5 2.5 0 1 1 3.5 2.3c-.6.3-1 .9-1 1.6v.6M12 17h.01" />
    </>
  ),
  logout: <path d="M15 4h3a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-3M10 17l-5-5 5-5M5 12h11" />,
  film: (
    <>
      <rect x="3" y="4" width="18" height="16" rx="2.5" />
      <path d="M7 4v16M17 4v16M3 9h4M3 15h4M17 9h4M17 15h4" />
    </>
  ),
  camera: (
    <>
      <path d="m3 6.5 13-3 2 7-13 3z" />
      <path d="m18 6 3-.7.8 3-3 .7M8 13l1.5 4.5M3 21v-4h6.5" />
    </>
  ),
};

/** An icon by name (an unknown name draws nothing). Decorative unless `label` is given. */
export function Icon({ name, size = 18, label, className }: { name: string; size?: number; label?: string; className?: string }) {
  return (
    <svg
      className={`op-icon ${className ?? ""}`}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    >
      {paths[name] ?? null}
    </svg>
  );
}

export const iconNames = Object.keys(paths);
