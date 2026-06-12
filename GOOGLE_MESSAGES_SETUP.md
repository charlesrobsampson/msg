# Google Messages Setup

`msg` integrates with Google Messages via [openmessage](https://github.com/MaxGhenis/openmessage/tree/main), a local server that bridges the Google Messages web API. This document covers initial setup and troubleshooting.

## Prerequisites

- Google account with Google Messages for Web enabled
- A phone with the Google Messages app, signed into the same account

## Quick start

**1. Enable the provider**

```bash
msg provider enable google
```

**2. Start the server and pair**

```bash
msg server start
msg pair google
```

`msg pair google` stops the server, launches the pairing flow, and displays a QR code. Scan it with the Google Messages app on your phone:

- Open Google Messages → tap your profile picture → **Messages for web** → **QR code scanner**

Once paired, the server restarts automatically.

*NOTE: on initial startup it will pull all your conversation history. This will take up some space and could potentially take a long time*

**3. Verify the connection**

```bash
msg provider status
```

Should print `Google Messages: Connected`.

## Configuration

Add a Google Messages provider to `~/.config/msg/config.json`:

```json
{
  "providers": [
    {
      "id": "sms",
      "type": "sms",
      "enabled": true,
      "port": 7007
    }
  ]
}
```

The `port` field is optional (defaults to `7007`). Override with the `OPENMESSAGES_PORT` environment variable if needed.

## Files

| Path | Purpose |
|---|---|
| `internal/openmessage/` | openmessage server source (git submodule — do not edit) |
| `internal/openmessage/om-server` | compiled binary (built on first `msg server start`) |
| `internal/openmessage/server.log` | server log output |

## Common operations

| Command | What it does |
|---|---|
| `msg server start` | Build and start the openmessage server |
| `msg server stop` | Stop the openmessage server |
| `msg server status` | Check whether the server is running |
| `msg pair google` | Re-register via QR code (needed after session expiry) |
| `msg provider reconnect google` | Soft reconnect using existing credentials |
| `msg provider disconnect google` | Unpair and remove stored credentials |

## Session expiry

Google Messages sessions expire periodically. When `msg provider status` shows `Disconnected` or `Needs Pairing`:

1. Try a soft reconnect first: `msg provider reconnect google`
2. If that fails, re-pair: `msg pair google`

## Background

openmessage implements the Google Messages for Web protocol to expose a local REST API. `msg` talks to this API to fetch conversations and send messages. The server binary is compiled from the submodule source at `internal/openmessage/` on first run.
