# tg-cli

A Telegram user client in one Go binary: commands for agents, a TUI for humans. Binary name `tg`.

## Layout

| Path | Role |
| --- | --- |
| `cmd/tg` | Entry point, holds the version |
| `internal/core` | Domain types, the `Backend` interface both front ends use, and the write guard |
| `internal/mtp` | Telegram backend (MTProto via gotd): connection, login, peers cache, conversions |
| `internal/demo` | In-memory backend with sample chats, used by `TG_DEMO=1`, tests and screenshots |
| `internal/cli` | Commands, flag parsing, text and JSON output |
| `internal/tui` | Bubble Tea v2 interface: `tui.go` state and input, `view.go` rendering |
| `internal/termimg` | Kitty graphics protocol (Unicode placeholders) for profile pictures |
| `internal/selfupdate` | Background and manual updates from GitHub releases |
| `internal/config` | File locations and `config.json`: credentials, accounts, guardrails |
| `install.sh`, `install.ps1` | One line installers |
| `scripts/` | Release build, terminal screenshots |
| `site/` | Landing page at tgc.li: static files in `public/`, served by the container in `compose.yml`. It also redirects `/install.sh` and `/install.ps1` to the scripts on main |

## Commands

```sh
go test ./...                       # must pass
go vet ./... && gofmt -l .          # must be clean
TG_DEMO=1 go run ./cmd/tg           # run without an account
scripts/build-release.sh v0.0.0     # cross compile every platform into dist/
```

## Rules

- Front ends only talk to `core.Backend`. Anything new the UI needs from Telegram goes through that interface and gets a demo implementation.
- Do not add dependencies that use `text/template` or `reflect.Value.MethodByName` (cobra does). They disable the linker's dead code elimination and the binary grows from 20 MB to 50 MB, because gotd's generated API is huge.
- Chat IDs follow the Bot API convention everywhere outside `internal/mtp`.
- `internal/cli/guide.txt` is how agents learn the tool. Any change to a command, flag or output format updates it in the same PR.
- Errors say what to do next, like "no chat matches, find it with `tg chats -q <text>`".
- Every write to Telegram goes through `core.Guard`. A new write operation is added to the guard in the same PR.
- Commands that loosen guardrails call `needPerson`. `config.json` stays the single source of truth: commands read it, change it and write it back, never cache it.
- Reading never marks messages as read unless asked. Sending by title only matches chats in the chat list.
- No continuously repainting animations in the TUI (spinners, blinking).
- Tests are focused on behavior that can break. No smoke tests.

## Pull requests

**Any change to the TUI or to the output of a command must be shown with screenshots of a real terminal, never with pasted text.** For a change, include before and after.

```sh
# 1. Take the screenshot. It always runs on demo data, never on a real account.
scripts/shot.sh /tmp/after.png --key down --key enter -- tg
scripts/shot.sh /tmp/chats.png -- tg chats -u

# 2. Upload it and get the Markdown to paste into the PR description.
scripts/shot-publish.sh /tmp/after.png /tmp/chats.png
```

`shot.sh` runs kitty headless (Linux with Xvfb and ffmpeg), so profile pictures show up as they do for users. Take the "before" shot from a checkout of the base branch. Never screenshot a real account: the repository is public.

Titles follow conventional commits, like `fix(tui): keep the cursor on the selected chat`.

## Releasing

Push a tag `vX.Y.Z` on main. The release workflow tests, cross compiles and publishes the binaries with `checksums.txt`. `install.sh` and `tg update` always fetch the latest release, so a release reaches every user within hours.

## Testing against Telegram

`TG_TEST_DC=1` connects to Telegram's test servers with the public test API ID. They no longer create accounts for the `99966XYYYY` test numbers, so authenticated calls need a real account: use a separate `TG_CLI_HOME` and send only to `me`.
