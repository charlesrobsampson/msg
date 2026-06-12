# msg

A terminal-first messaging client for Google Messages and Signal (maybe more one day?). Provides both a full TUI and a clean CLI for scripting and quick access.

![you forgot the msg](msg.jpg)

## Features

- Unified inbox across Google Messages (SMS/RCS) and Signal
- Full TUI with conversation list, message thread, draft composer, emoji picker, and reaction viewer
- CLI for listing, reading, searching, and sending messages
- Alias shortcuts for frequently messaged contacts
- Multi-account profile support
- Signal markdown rendering (bold, italic, spoilers, monospace) in both TUI and CLI

## Prerequisites

- Go 1.25+
- Docker (for Signal)
- A phone with Google Messages or Signal installed

## Installation

```bash
go install github.com/charlesrobsampson/msg/cmd/msg@latest
```

Or clone and build locally (required if using Google Messages, since the `openmessage` submodule must be compiled):

```bash
git clone --recurse-submodules https://github.com/charlesrobsampson/msg
cd msg
go build -o msg ./cmd/msg
mv msg $(go env GOPATH)/bin/
```

> **Note:** If you already cloned without `--recurse-submodules`, run `git submodule update --init --recursive` before building.

## Provider setup

`msg` supports two providers. Follow the guide for whichever you want to use — both can be active at the same time.

| Provider | Guide |
|---|---|
| Google Messages (SMS/RCS) | [GOOGLE_MESSAGES_SETUP.md](GOOGLE_MESSAGES_SETUP.md) |
| Signal | [SIGNAL_SETUP.md](SIGNAL_SETUP.md) |

## Quick start

**1. Enable a provider**

```bash
msg provider enable google    # then: msg pair google
msg provider enable signal    # then: msg link signal
```

**2. Start backend services**

```bash
msg server start
```

**3. Open the TUI**

```bash
msg
```

Or use the CLI directly:

```bash
msg unread              # see what's new
msg list                # list all conversations
msg read alice          # read a conversation (by alias or ID)
msg send alice "hey" -s # send a message
```

## TUI

Running `msg` with no arguments opens the TUI. Navigation:

| Key | Action |
|---|---|
| `Tab` / `Shift+Tab` | Switch between views (Conversations, Unreads, Contacts, Aliases, Styles, Providers) |
| `↑` / `↓` | Move between conversations |
| `Enter` | Open selected conversation / confirm action |
| `Tab` (in conversation) | Focus message thread for scrolling |
| `d` | Open draft composer for selected conversation |
| `Ctrl+E` | Open emoji picker (in draft mode) |
| `e` | Toggle reaction detail popup |
| `m` | Toggle conversation metadata |
| `/` | Filter conversations |
| `n` | New message |
| `q` / `Ctrl+C` | Quit |

## CLI reference

```
msg [-p <profile>] <command> [flags]
```

Run `msg <command> --help` for detailed usage and examples for any command.

### Messaging

| Command | Description |
|---|---|
| `list [-l <n>]` | List conversations |
| `unread [-c]` | Show unread messages (`-c` for count only) |
| `search <query> [-l <n>]` | Search conversations by name |
| `read <id\|alias> [-l <n>] [-u]` | Read a conversation |
| `send <id\|alias> <body> [-s] [-a paths]` | Send a message (dry-run without `-s`) |
| `contacts <query> [-l <n>]` | Search address book |
| `quick-send <name> <body> [-s]` | Look up contact and send |
| `alias <shortcut> <conv_id> <platform>` | Create a conversation alias |

### Provider management

| Command | Description |
|---|---|
| `provider status` | Show connection status for all providers |
| `provider enable <google\|signal>` | Enable a provider |
| `provider disable <google\|signal>` | Disable a provider |
| `provider reconnect google` | Soft reconnect without re-pairing |
| `provider disconnect google` | Unpair and remove credentials |
| `pair google` | Re-register via QR code |
| `link signal` | Link a Signal device |

### Server

| Command | Description |
|---|---|
| `server start` | Start all backend services |
| `server stop` | Stop all backend services |
| `server status` | Show service health |

### Common flags

| Flag | Commands | Description |
|---|---|---|
| `-p`, `--profile <name>` | all | Use a named config profile |
| `-l`, `--limit <n>` | list, search, read, contacts | Max results |
| `-s`, `--send` | send, quick-send | Commit and transmit (omit for dry-run) |
| `-u`, `--leave-unread` | read | Don't mark conversation as read |
| `-c`, `--count` | unread | Print count only |
| `-a`, `--attach <paths>` | send | Comma-separated file paths to attach |

## Aliases

Aliases let you use short names instead of raw conversation IDs:

```bash
msg alias alice signal:+15551234567 signal
msg send alice "hey" -s
msg read alice
```

Aliases are resolved automatically in `send`, `read`, and `quick-send`.

## Profiles

Multiple accounts are supported via profiles. Add a `profiles` key to `~/.config/msg/config.json`:

```json
{
  "profiles": {
    "work": {
      "account": "+1XXXXXXXXXX",
      "providers": { ... }
    }
  }
}
```

Switch profiles with the `-p` flag or the `MSG_PROFILE` environment variable:

```bash
msg -p work unread
MSG_PROFILE=work msg
```

## Configuration

```bash
msg config                    # show current settings
msg config set editor nvim    # set the editor for draft mode
```

Config lives at `~/.config/msg/config.json`.
