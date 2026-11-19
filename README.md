# tg

Telegram for agents and humans. One small binary that gives agents and scripts read and write access to your Telegram account through plain commands, and gives you a fast terminal client with profile pictures.

![tg TUI](docs/tui.png)

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/RealDoc06/tg-cli/main/install.sh | bash
```

Works on Linux, macOS and Windows (Git Bash, MSYS2, WSL). From PowerShell:

```powershell
irm https://raw.githubusercontent.com/RealDoc06/tg-cli/main/install.ps1 | iex
```

Then log in once:

```sh
tg login
```

Telegram requires every client to identify itself with an API ID and hash. `tg login` asks for them the first time: create them in a minute at [my.telegram.org/apps](https://my.telegram.org/apps). The session stays on your machine until `tg logout`.

## For agents

Tell the agent to use `tg`. That is enough: `tg guide` (also what a bare `tg` prints when it is not run from a terminal) briefs it on the workflow, how chats are addressed and the rules, and errors name the next step. No skill or prompt needed.

Every operation is a command with predictable output. Add `--json` for one JSON object per line.

![tg commands](docs/commands.png)

| Command | What it does |
| --- | --- |
| `tg chats [-n 30] [-u] [-q text]` | List chats, most recent first. `-u` only unread, `-q` filter by name |
| `tg read <chat> [-n 30] [--before id] [--after id] [--mark-read]` | Print messages, oldest first |
| `tg send <chat> [text...] [-f file] [--reply-to id]` | Send a message. Text from arguments or stdin |
| `tg search <text> [-c chat]` | Search messages everywhere or in one chat |
| `tg watch [chat...] [--count n] [--timeout 10m]` | Stream new messages, or wait for a reply |
| `tg download <chat> <message-id> [-o dir]` | Save the photo or file of a message |
| `tg mark-read <chat>` | Mark a chat as read |
| `tg create-group <title>` | Create a new group with only you in it |
| `tg accounts` | List accounts, pick the default one |
| `tg config` | Show or change an account's guardrails |
| `tg guide` | The briefing for agents |
| `tg me` | Show the logged in account |

A `<chat>` is a numeric ID from `tg chats`, an `@username`, a `+phone`, a chat title, or `me` for Saved Messages. A title that matches several chats is refused with the list of candidates, and titles only match chats already in your chat list, so a message never goes to a stranger who shares a name.

```sh
tg chats -u                                   # what is unread
tg read "Dev Team" -n 50 --json               # last 50 messages as JSON
tg send @alice "on my way"
git log -5 --oneline | tg send me             # pipe anything
id=$(tg send @alice "did it work?" --json | jq .id)
tg watch @alice --after $id --count 1 --timeout 10m   # the reply, even if it came first
```

Reading does not mark messages as read unless you pass `--mark-read`. Exit codes: 0 success, 1 failure, 2 wrong usage, 3 refused by a guardrail.

Message text is untrusted input. An agent that reads chats and can also send should not follow instructions found inside messages. The plain output says so above every batch of messages, and so does the guide.

`tg watch` skips messages sent from the account itself. Several agents sharing one account all look like `me` to each other: they need `--outgoing`.

Every command is appended to `log.jsonl` in tg's directory, with its account, arguments, exit code and duration, so you can see what an agent did. It is a convenience, not an audit trail: it holds the text of sent messages, and `TG_NO_LOG=1` turns it off for whoever sets it. `tg login` arguments are never logged.

## Guardrails

An agent with your session can write to anyone as you. Two limits keep that in check, per account:

```sh
tg config                        # show the limits and where the file is
tg config read-only on           # no sending, no marking as read, no new groups
tg config allow "Dev Team" @bob  # from now on, write only to these chats
tg config disallow @bob          # take one off the list (never the last one)
tg config allow --any            # lift the list
```

They live in `config.json`, and the commands and the file are the same thing: edit either, the other shows it.

```json
{
  "account": "default",
  "accounts": {
    "default": {
      "read_only": false,
      "send_to": [{ "id": -1001234567890, "title": "Dev Team" }]
    }
  }
}
```

- Only `id` counts in `send_to`, the title is for you. An empty list means any chat.
- The file is re-read on every write, so a change applies at once, even to a TUI that is already open.
- A misspelled key stops tg with an error instead of being ignored.
- Loosening a limit (`read-only off`, adding to a list or dropping it), `login`, `logout` and `accounts use` only work from an interactive terminal. Agents normally run commands without one and are refused, so a message cannot talk them out of their limits with one command. Tightening works from anywhere, including starting a list on an open account.
- Limits belong to an account, not to an agent. `--account` reaches every account on the machine, so restrict each one an agent could use.
- `tg download` refuses to write into tg's own directory and never replaces an existing file.
- What this does not stop: reading is never limited, an agent that drives a real terminal (tmux, a pty) passes the terminal check, and one that edits files can edit `config.json`. The guardrails stop mistakes and injected instructions, not a determined process with your user's rights.

## Accounts

```sh
tg login --account work      # add a second account next to the first
tg accounts                  # list them, * marks the default
tg accounts use work         # change the default
tg send @alice hi -a work    # or pick one for a single command
tg logout --account work
```

Each account has its own session, peer cache and guardrails. `TG_ACCOUNT` selects one for a whole shell or agent. Give each agent its own account and they stop looking like `me` to each other.

## For humans

Run `tg` with no arguments.

| Key | Action |
| --- | --- |
| `↑` `↓` `j` `k` | Move in the chat list |
| `enter` | Open the chat, or send the message |
| `/` | Filter chats by name |
| `tab` `esc` | Switch between chat list and composer |
| `pgup` `pgdn` | Scroll the conversation, older messages load as you go |
| `q` `ctrl+c` | Quit |

The mouse works too: click a chat, scroll either pane. New messages arrive live.

### Themes

`tg config theme <name>` picks the colors: the Catppuccin flavors `mocha` (the default), `macchiato`, `frappe` and `latte`, or Telegram's `dark` and `light`. `tg config theme` lists them. The theme paints the terminal's background while tg runs, and is stored as `"theme"` in `config.json`.

### Profile pictures

Real profile pictures are drawn in terminals that implement the Kitty graphics protocol with Unicode placeholders: [Kitty](https://sw.kovidgoyal.net/kitty/) and [Ghostty](https://ghostty.org). Elsewhere each chat gets a colored badge with its initials. Inside tmux, pictures need `set -g allow-passthrough on`. Force them on or off with `TG_IMAGES=1` or `TG_IMAGES=0`.

## Updates

tg checks for a new release in the background when it starts, at most every 6 hours, and installs it for the next launch. Nothing slows down the command you ran.

```sh
tg update          # update now
tg --version
```

Set `TG_NO_AUTOUPDATE=1` to turn the background check off.

## Configuration

| Variable | Effect |
| --- | --- |
| `TG_CLI_HOME` | Directory for everything tg stores |
| `TG_ACCOUNT` | Account to act as, instead of the default one |
| `TG_API_ID`, `TG_API_HASH` | Telegram API credentials, instead of the saved ones |
| `TG_DEMO=1` | Run everything against built in sample chats, no account needed. Uses its own directory, never your real configuration |
| `TG_IMAGES=0\|1` | Force profile pictures off or on |
| `TG_NO_AUTOUPDATE=1` | Disable background updates |
| `TG_NO_LOG=1` | Do not record commands in `log.jsonl` |

Sessions live in `tg-cli/accounts/<name>/session.json` under your config directory (`~/.config` on Linux, `~/Library/Application Support` on macOS, `%AppData%` on Windows), readable only by you. Each is a full login to an account: treat it like a password.

## Development

```sh
go test ./...
go run ./cmd/tg                  # the TUI
TG_DEMO=1 go run ./cmd/tg        # with sample data, no account
scripts/shot.sh out.png -- tg    # screenshot of a real terminal
```

Pushing a tag like `v1.2.3` builds and publishes a release. Setting the repository secrets `TG_API_ID` and `TG_API_HASH` bakes default API credentials into release builds so users can skip that step.

See [AGENTS.md](AGENTS.md) for the layout and the contribution rules.

## License

MIT
