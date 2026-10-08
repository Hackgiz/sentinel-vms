# Sentinel VMS for Linux

A headless video management server with a web dashboard. Same recording
layout, roles and audit model as the Mac app; the iPhone app will pair to it
the same way (milestone M4). See [ROADMAP.md](ROADMAP.md).

## Install (systemd)

```sh
tar xzf sentinel-<version>-linux-amd64.tar.gz   # or -arm64 for Raspberry Pi 4/5, ARM servers
cd sentinel-<version>-linux-amd64
sudo ./install.sh
```

Then open `http://<server-ip>:8090` and create the administrator account.

- Service: `systemctl status sentinel`, logs: `journalctl -u sentinel -f`
- Data (database, install key, recordings): `/var/lib/sentinel`
- Runs as the unprivileged `sentinel` user with a hardened systemd sandbox.

## Run without installing

```sh
./sentinel -data ~/sentinel-data -listen :8090
```

`mediamtx` must sit next to `sentinel`, in `<data>/bin/`, or on `PATH`.

## Docker

```sh
docker buildx build --platform linux/amd64,linux/arm64 -t sentinel dist/docker
docker run -d --name sentinel -p 8090:8090 -v sentinel-data:/data --restart unless-stopped sentinel
```

## Security model

- Only port 8090 is exposed. MediaMTX listens on 127.0.0.1; all video goes
  through Sentinel's authenticated proxy.
- Passwords: bcrypt. Sessions: random tokens, stored hashed, HttpOnly +
  SameSite=Strict cookies. Sign-in lockout after 5 failures (per user and IP).
- Camera passwords are encrypted at rest (AES-256-GCM) with a per-install key
  (`install.key`, mode 0600). Back it up together with `sentinel.db`.
- Audit log entries are SHA-256 hash-chained; *Audit Log → Verify chain*
  detects edits or deletions made outside Sentinel.
- For access outside your network, put it behind HTTPS (the Cloudflare tunnel
  integration arrives in M4). Don't port-forward plain HTTP.

## Develop

```sh
export PATH=$HOME/.local/go/bin:$PATH
go test ./...
go run ./cmd/sentinel -listen 127.0.0.1:8091 -data /tmp/sentinel-dev   # API + embedded dashboard
(cd web && npm run dev)                                                 # hot-reload UI on :5173
VERSION=0.1.0 scripts/build.sh                                         # release archives
```
