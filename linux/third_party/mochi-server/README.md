# mochi-mqtt/server v2.7.9 (patched copy)

A pruned copy of [github.com/mochi-mqtt/server/v2](https://github.com/mochi-mqtt/server) v2.7.9
(MIT, see LICENSE.md), wired in with a `replace` directive in `../../go.mod`.
It is only used as the embedded broker in `internal/mqtt` tests; production
Sentinel talks to an external broker over paho.

## Why it exists

v2.7.9 has a data race that `go test -race` catches intermittently in CI:
`TopicsIndex.Messages` → `scanMessages` reads `particle.retainPath` with no
lock, while `RetainMessage` writes it under the root particle's mutex. It
fires when a client subscribes with a wildcard (the retained-message scan)
while another publishes a retained message — which our Home-Assistant-style
tests do constantly. The broker sends SUBACK *before* scanning retained
messages, so tests cannot sequence around it.

## The patch

One change: `Messages` in `topics.go` takes `x.root.Lock()` for the scan,
matching the locking every mutator (`Subscribe`, `Unsubscribe`,
`RetainMessage`, `InlineSubscribe`, `InlineUnsubscribe`) already uses.
Everything else is byte-identical upstream code, minus tests, examples,
`cmd/`, `config/`, and the hooks we don't import.

## Removing it

When an upstream release fixes the race, delete this directory and the
`replace` line in `linux/go.mod`, bump the require, and run
`go test -race ./internal/mqtt/ -count=10` to confirm.
