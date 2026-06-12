# Signal Setup

`msg` integrates with Signal via [signal-cli](https://github.com/AsamK/signal-cli) running as a local REST API container. This document covers initial setup and troubleshooting.

**API reference:** [signal-cli REST API docs](https://bbernhard.github.io/signal-cli-rest-api/)

## Prerequisites

- Docker (with Compose)
- A Signal account (phone number) to register

## Quick start

The config files live in `~/.config/msg/`.

**1. Register or link a device**

Start the unpatched image temporarily to register:

```bash
docker compose -f ~/.config/msg/docker-compose-signal.yml up -d
# Follow signal-cli registration steps via the REST API, then stop it
docker compose -f ~/.config/msg/docker-compose-signal.yml down
```

Or copy an existing `signal-data/` directory from another device:

```bash
mkdir -p ~/.config/msg/signal-data
# copy your accounts.json and account subdirectories here
```

**2. Configure msg**

Add a Signal provider to `~/.config/msg/config.json`:

```json
{
  "account": "+1XXXXXXXXXX",
  "providers": [
    {
      "id": "signal",
      "type": "signal",
      "enabled": true,
      "port": 18081
    }
  ]
}
```

**3. Build and run the patched image**

Signal's server periodically changes its envelope format in ways that break signal-cli. The patched image applies a null-safety fix to `SignalServiceMetadataProtobufSerializer` so messages aren't silently dropped (see [Background](#background) below).

```bash
docker compose -f ~/.config/msg/docker-compose-signal.yml build --no-cache
docker compose -f ~/.config/msg/docker-compose-signal.yml down
docker compose -f ~/.config/msg/docker-compose-signal.yml up -d
```

The build takes about 2 minutes. It only needs GitHub access — no Maven Central required.

**4. Start the receiver**

The receiver listens for incoming webhooks and maintains the WebSocket subscription that activates message delivery:

```bash
msg signal-receiver
```

Run this in a background process or add it to your shell startup. `msg server start` will launch it automatically alongside the signal-cli container.

## Files

| Path | Purpose |
|---|---|
| `~/.config/msg/docker-compose-signal.yml` | Compose file for signal-manager |
| `~/.config/msg/Dockerfile.signal-patched` | Multi-stage build that patches the Turasa JAR |
| `~/.config/msg/signal-data/` | signal-cli account data (persisted as a Docker volume) |

## Multiple accounts

Multiple Signal accounts can share one signal-cli instance. Each account gets its own subdirectory under `signal-data/`. The receiver automatically subscribes all registered accounts on startup by calling `GET /v1/accounts`.

Map additional accounts to `msg` profiles in `~/.config/msg/config.json`:

```json
{
  "profiles": {
    "chazzybot": {
      "account": "+1XXXXXXXXXX",
      "providers": [...]
    }
  }
}
```

Switch profiles with `MSG_PROFILE=chazzybot msg`.

## Rebuilding after a Signal server update

If messages stop arriving and you see `NullPointerException` / `must not be null` errors in `docker logs signal-manager`, Signal has changed its envelope format again. Rebuild:

```bash
docker compose -f ~/.config/msg/docker-compose-signal.yml build --no-cache
docker compose -f ~/.config/msg/docker-compose-signal.yml down
docker compose -f ~/.config/msg/docker-compose-signal.yml up -d
```

If the error is in a different field than `serverGuid`, update the patch in `Dockerfile.signal-patched` — look for the corresponding line in `SignalServiceMetadataProtobufSerializer.kt` and apply the same `?: ""` (for String fields) or `!!` (for object fields) pattern.

## Background

Signal-cli depends on [Turasa/libsignal-service-java](https://github.com/Turasa/libsignal-service-java) for the Signal protocol. In June 2026, Signal's server stopped including `serverGuid` in some envelope types. The `SignalServiceMetadataProtobufSerializer` class called `.serverGuid(metadata.serverGuid)` without a null check, throwing a `NullPointerException` for every affected message and silently dropping it.

The fix (`.serverGuid(metadata.serverGuid ?: "")`) is backward-compatible with the existing type signatures in `unofficial_147`, so we patch that version rather than upgrading to `unofficial_148` which changed the type signature in a way that broke signal-cli v0.14.4.1.

The patched class is compiled with the standalone Kotlin compiler (same version as the base image, auto-detected) and injected directly into `signal-service-java-2.15.3_unofficial_147.jar` using `jar uf`. No Maven Central access is required.
