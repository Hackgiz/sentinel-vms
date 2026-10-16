# Sentinel Operator for Linux (Electron)

Live view, playback and alarm handling. Targets: AppImage and .deb, x64 and arm64.

## Build

```sh
npm ci
npm run build          # typecheck + bundle (also builds ../web operator page)
npm run pack           # unpacked app for this machine, for a quick check
```

### AppImage (x86_64 + arm64) - can be built on macOS or Linux

```sh
npm run build
npx electron-builder --linux AppImage --x64 --arm64
```

`electron-builder.yml` sets `toolsets.appimage: 1.0.3` (static type-2 runtime).
The default runtime dynamically links `libz.so` (the dev symlink, only present
with `zlib1g-dev`), so the AppImage failed on stock Ubuntu with
"error while loading shared libraries: libz.so". Do not remove that setting.

### .deb (amd64 + arm64) - must be built on Linux

fpm needs GNU tar, so the .deb cannot be built on macOS. On a Linux machine
(Node 24 LTS):

```sh
npm ci && npm run build
npx electron-builder --linux deb --x64 --arm64
```

If `npm run build` cannot see `../web` (project copied alone), build on the Mac
and copy `build/` over, then run only the `electron-builder` line.

Artifacts land in `release/` (gitignored):
`Sentinel Operator-<version>-{x86_64,arm64}.AppImage`, `...-{amd64,arm64}.deb`.
Both are the same `build/` bundle, so build once and package everywhere.

## Install

- AppImage: `chmod +x` and run. Needs FUSE (`fuse3`/`fusermount3`, present on
  stock Ubuntu desktop); otherwise run with `--appimage-extract-and-run`.
- .deb: `sudo apt install ./Sentinel-Operator-<version>-<arch>.deb`
