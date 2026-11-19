// Package cli defines the tg command line: one subcommand per Telegram
// operation for agents and scripts, and the TUI when run bare in a terminal.
package cli

import (
	"cmp"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/RealDoc06/tg-cli/internal/config"
	"github.com/RealDoc06/tg-cli/internal/core"
	"github.com/RealDoc06/tg-cli/internal/demo"
	"github.com/RealDoc06/tg-cli/internal/mtp"
	"github.com/RealDoc06/tg-cli/internal/selfupdate"
	"github.com/RealDoc06/tg-cli/internal/tui"
)

// app carries what every command needs.
type app struct {
	version string
	json    bool
	// account is the one asked for with --account, empty for the default.
	account string
}

// command is one subcommand. Its flags are parsed anywhere on the line, so
// "tg read alice -n 5" and "tg read -n 5 alice" are the same.
type command struct {
	use     string // name and arguments, like "read <chat>"
	short   string
	long    string
	example string
	minArgs int
	maxArgs int // -1 for any number
	flags   *pflag.FlagSet
	run     func(ctx context.Context, args []string) error
}

func (c *command) name() string { return strings.Fields(c.use)[0] }

// newCommand creates a command with the flags every command accepts.
func (a *app) newCommand(use, short string, minArgs, maxArgs int) *command {
	c := &command{use: use, short: short, minArgs: minArgs, maxArgs: maxArgs}
	c.flags = pflag.NewFlagSet(c.name(), pflag.ContinueOnError)
	c.flags.SortFlags = false
	c.flags.SetOutput(io.Discard)
	c.flags.BoolVar(&a.json, "json", false, "print one JSON object per line")
	c.flags.StringVarP(&a.account, "account", "a", "", "act as this account instead of the default one")
	c.flags.BoolP("help", "h", false, "show this help")
	return c
}

func (c *command) help() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nUsage:\n  tg %s [flags]\n", cmp.Or(c.long, c.short), c.use)
	if c.example != "" {
		fmt.Fprintf(&b, "\nExamples:\n%s\n", c.example)
	}
	fmt.Fprintf(&b, "\nFlags:\n%s", c.flags.FlagUsages())
	return b.String()
}

// guide is the briefing printed by `tg guide`, and by a bare `tg` when it is
// not run from a terminal, which is how an agent usually meets the tool.
//
//go:embed guide.txt
var guide string

const intro = `tg gives agents and scripts read and write access to a Telegram account,
and gives humans a terminal client. Run it bare for the TUI.

Chats are addressed by numeric ID (see "tg chats"), @username, +phone,
title, or "me" for Saved Messages.`

func rootHelp(commands []*command) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nAgents: run `tg guide` first, it is the short version of everything here.\n\nUsage:\n  tg                    open the TUI\n  tg <command> [flags]\n\nCommands:\n", intro)
	for _, c := range commands {
		fmt.Fprintf(&b, "  %-12s %s\n", c.name(), c.short)
	}
	fmt.Fprintf(&b, "  %-12s %s\n", "guide", "How an agent should use tg, start here")
	b.WriteString("\nFlags:\n      --json           print one JSON object per line\n  -a, --account name   act as this account instead of the default one\n  -h, --help           show help, also per command\n  -v, --version        show the version\n")
	return b.String()
}

// errUsage marks mistakes in how tg was called.
type errUsage struct{ error }

// Main runs the command line and returns the process exit code.
func Main(version string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := &app{version: version}
	started := time.Now()
	err := a.dispatch(ctx, os.Args[1:])
	code := exitCode(ctx, err)
	account := ""
	if name, _, activeErr := a.active(); activeErr == nil {
		account = name
	}
	logInvocation(account, os.Args[1:], started, code, err)
	if err != nil && code != 130 {
		fmt.Fprintln(os.Stderr, "tg:", err)
	}
	return code
}

func exitCode(ctx context.Context, err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(ctx.Err(), context.Canceled):
		return 130 // interrupted, nothing to report
	case errors.As(err, new(errUsage)):
		return 2
	case errors.As(err, new(*core.RefusedError)):
		return 3
	}
	return 1
}

func (a *app) dispatch(ctx context.Context, args []string) error {
	commands := []*command{
		a.loginCmd(), a.logoutCmd(), a.meCmd(), a.chatsCmd(), a.readCmd(), a.sendCmd(),
		a.searchCmd(), a.markReadCmd(), a.createGroupCmd(), a.downloadCmd(), a.watchCmd(),
		a.accountsCmd(), a.configCmd(), a.updateCmd(),
	}

	// Flags before the command name belong to tg itself.
	root := pflag.NewFlagSet("tg", pflag.ContinueOnError)
	root.SetInterspersed(false)
	root.SetOutput(io.Discard)
	root.BoolVar(&a.json, "json", false, "")
	root.StringVarP(&a.account, "account", "a", "", "")
	help := root.BoolP("help", "h", false, "")
	showVersion := root.BoolP("version", "v", false, "")
	if err := root.Parse(args); err != nil {
		return errUsage{err}
	}
	args = root.Args()
	if err := config.Migrate(); err != nil {
		return err
	}

	switch {
	case *showVersion || (len(args) > 0 && args[0] == "version"):
		fmt.Println("tg", a.version)
		return nil
	case *help || (len(args) == 1 && args[0] == "help"):
		fmt.Print(rootHelp(commands))
		return nil
	case len(args) == 1 && args[0] == "guide":
		fmt.Print(guide)
		return nil
	case len(args) == 0:
		if !term.IsTerminal(int(os.Stdout.Fd())) {
			fmt.Print(guide)
			return nil
		}
		selfupdate.AutoCheck(a.version)
		return a.backend(ctx, true, func(ctx context.Context, be core.Backend) error {
			// Naming the account only matters when there is a choice.
			label := ""
			if name, cfg, err := a.active(); err == nil && len(cfg.Accounts) > 1 {
				label = name
			}
			return tui.Run(ctx, be, a.version, label)
		})
	}

	name, wantHelp := args[0], false
	if name == "help" {
		name, wantHelp, args = args[1], true, args[1:]
	}
	i := slices.IndexFunc(commands, func(c *command) bool { return c.name() == name })
	if i < 0 {
		return errUsage{fmt.Errorf("unknown command %q, see `tg guide`", name)}
	}
	cmd := commands[i]
	// Group and channel IDs are negative numbers. Shield them so they are
	// read as arguments, not as flags.
	rest := slices.Clone(args[1:])
	for i, arg := range rest {
		if negativeID.MatchString(arg) {
			rest[i] = shield + arg
		}
	}
	if err := cmd.flags.Parse(rest); err != nil {
		return errUsage{fmt.Errorf("%s, see `tg %s --help`", strings.ReplaceAll(err.Error(), shield, ""), name)}
	}
	positional := cmd.flags.Args()
	for i, arg := range positional {
		positional[i] = strings.TrimPrefix(arg, shield)
	}
	// A shielded ID can also be the value of a flag, as in --chat -100123.
	cmd.flags.Visit(func(f *pflag.Flag) {
		if value, shielded := strings.CutPrefix(f.Value.String(), shield); shielded {
			_ = f.Value.Set(value)
		}
	})
	if h, _ := cmd.flags.GetBool("help"); h || wantHelp {
		fmt.Print(cmd.help())
		return nil
	}
	if n := cmd.flags.NArg(); n < cmd.minArgs || (cmd.maxArgs >= 0 && n > cmd.maxArgs) {
		return errUsage{fmt.Errorf("usage: tg %s, see `tg %s --help`", cmd.use, name)}
	}
	if name != "update" {
		selfupdate.AutoCheck(a.version)
	}
	return cmd.run(ctx, positional)
}

var negativeID = regexp.MustCompile(`^-\d+$`)

// shield prefixes negative numbers while flags are parsed.
const shield = "\x00"

// active returns the account to act as, and the configuration as loaded.
func (a *app) active() (string, *config.File, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", nil, err
	}
	name := cfg.Active(a.account)
	if err := config.CheckName(name); err != nil {
		return "", nil, errUsage{err}
	}
	return name, cfg, nil
}

// backend opens the active account on Telegram, or the demo data when
// TG_DEMO is set, behind the account's guardrails.
func (a *app) backend(ctx context.Context, updates bool, fn func(context.Context, core.Backend) error) error {
	name, cfg, err := a.active()
	if err != nil {
		return err
	}
	if cfg.Accounts[name] == nil && name != config.DefaultAccount {
		return fmt.Errorf("no account %q, see `tg accounts`", name)
	}
	guard := func(be core.Backend) core.Backend {
		return core.Guard(be, func() (core.Policy, error) { return loadPolicy(name) })
	}
	if os.Getenv("TG_DEMO") != "" {
		return fn(ctx, guard(demo.New()))
	}
	err = mtp.Run(ctx, mtp.Options{Account: name, Version: a.version, Updates: updates}, func(ctx context.Context, be core.Backend) error {
		rememberUser(cfg, name, be.Self())
		return fn(ctx, guard(be))
	})
	if errors.Is(err, core.ErrNotLoggedIn) && name != config.DefaultAccount {
		return fmt.Errorf("account %q is not logged in, run `tg login --account %s`", name, name)
	}
	return err
}

func (a *app) meCmd() *command {
	c := a.newCommand("me", "Show the logged in account", 0, 0)
	c.run = func(ctx context.Context, _ []string) error {
		return a.backend(ctx, false, func(_ context.Context, be core.Backend) error {
			return a.printUser(be.Self())
		})
	}
	return c
}

func (a *app) chatsCmd() *command {
	c := a.newCommand("chats", "List chats, most recent first", 0, 0)
	limit := c.flags.IntP("limit", "n", 30, "maximum number of chats")
	unread := c.flags.BoolP("unread", "u", false, "only chats with unread messages")
	query := c.flags.StringP("query", "q", "", "only chats whose title or username contains this")
	c.run = func(ctx context.Context, _ []string) error {
		return a.backend(ctx, false, func(ctx context.Context, be core.Backend) error {
			// Filters apply after fetching, so look further back for them.
			fetch := *limit
			if *unread || *query != "" {
				fetch = max(*limit, 300)
			}
			chats, err := be.Chats(ctx, core.ChatQuery{Limit: fetch})
			if err != nil {
				return err
			}
			needle := strings.ToLower(*query)
			var out []core.Chat
			for _, chat := range chats {
				matches := strings.Contains(strings.ToLower(chat.Title), needle) || strings.Contains(strings.ToLower(chat.Username), needle)
				if matches && (!*unread || chat.Unread > 0) && len(out) < *limit {
					out = append(out, chat)
				}
			}
			return a.printChats(out)
		})
	}
	return c
}

func (a *app) readCmd() *command {
	c := a.newCommand("read <chat>", "Print the latest messages of a chat, oldest first", 1, 1)
	var q core.HistoryQuery
	c.flags.IntVarP(&q.Limit, "limit", "n", 30, "maximum number of messages")
	c.flags.IntVar(&q.BeforeID, "before", 0, "only messages older than this message ID")
	c.flags.IntVar(&q.AfterID, "after", 0, "only messages newer than this message ID")
	markRead := c.flags.Bool("mark-read", false, "mark the chat as read up to the last printed message")
	c.run = func(ctx context.Context, args []string) error {
		return a.backend(ctx, false, func(ctx context.Context, be core.Backend) error {
			chat, err := be.Resolve(ctx, args[0])
			if err != nil {
				return err
			}
			msgs, err := be.History(ctx, chat.ID, q)
			if err != nil {
				return err
			}
			if *markRead && len(msgs) > 0 {
				if err := be.MarkRead(ctx, chat.ID, msgs[len(msgs)-1].ID); err != nil {
					return err
				}
			}
			return a.printMessages(&chat, msgs)
		})
	}
	return c
}

func (a *app) sendCmd() *command {
	c := a.newCommand("send <chat> [text...]", "Send a message, text comes from the arguments or from stdin", 1, -1)
	c.example = `  tg send @alice "on my way"
  tg send me --file report.pdf "today's report"
  git log -5 --oneline | tg send "Dev Team"
  tg send @alice <<'EOF'
  several lines, "quotes" and $ signs arrive exactly as written
  EOF`
	var req core.SendRequest
	c.flags.IntVar(&req.ReplyTo, "reply-to", 0, "message ID to reply to")
	c.flags.StringVarP(&req.File, "file", "f", "", "file to attach, the text becomes its caption")
	c.run = func(ctx context.Context, args []string) error {
		req.Text = strings.Join(args[1:], " ")
		if len(args) == 1 || req.Text == "-" {
			req.Text = ""
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				data, err := io.ReadAll(os.Stdin)
				if err != nil {
					return err
				}
				req.Text = strings.TrimRight(string(data), "\r\n")
			}
		}
		if req.Text == "" && req.File == "" {
			return errUsage{errors.New("nothing to send: give the text as arguments, on stdin, or attach a --file")}
		}
		return a.backend(ctx, false, func(ctx context.Context, be core.Backend) error {
			chat, err := be.Resolve(ctx, args[0])
			if err != nil {
				return err
			}
			sent, err := be.Send(ctx, chat.ID, req)
			if err != nil {
				return err
			}
			return a.printSent(chat, sent)
		})
	}
	return c
}

func (a *app) searchCmd() *command {
	c := a.newCommand("search <text...>", "Search messages everywhere or in one chat", 1, -1)
	c.long = `Search messages in every chat, or in one with --chat. Telegram does the
matching: by whole words and their beginnings and inflections, not by
substring, so "introduce" also finds "introducing". Newest matches are kept
when there are more than --limit.`
	chatRef := c.flags.StringP("chat", "c", "", "search only in this chat")
	limit := c.flags.IntP("limit", "n", 30, "maximum number of messages")
	c.run = func(ctx context.Context, args []string) error {
		return a.backend(ctx, false, func(ctx context.Context, be core.Backend) error {
			q := core.SearchQuery{Text: strings.Join(args, " "), Limit: *limit}
			var chat *core.Chat
			if *chatRef != "" {
				resolved, err := be.Resolve(ctx, *chatRef)
				if err != nil {
					return err
				}
				chat, q.ChatID = &resolved, resolved.ID
			}
			msgs, err := be.Search(ctx, q)
			if err != nil {
				return err
			}
			if chat != nil {
				for i := range msgs {
					msgs[i].Chat = "" // the header already names the only chat
				}
			}
			if !a.json && chat == nil {
				fmt.Printf("# %d messages found\n", len(msgs))
			}
			return a.printMessages(chat, msgs)
		})
	}
	return c
}

func (a *app) markReadCmd() *command {
	c := a.newCommand("mark-read <chat>", "Mark every message of a chat as read", 1, 1)
	c.run = func(ctx context.Context, args []string) error {
		return a.backend(ctx, false, func(ctx context.Context, be core.Backend) error {
			chat, err := be.Resolve(ctx, args[0])
			if err != nil {
				return err
			}
			last, err := be.History(ctx, chat.ID, core.HistoryQuery{Limit: 1})
			if err != nil || len(last) == 0 {
				return err
			}
			return be.MarkRead(ctx, chat.ID, last[0].ID)
		})
	}
	return c
}

func (a *app) createGroupCmd() *command {
	c := a.newCommand("create-group <title...>", "Create a new group with only you in it", 1, -1)
	c.run = func(ctx context.Context, args []string) error {
		return a.backend(ctx, false, func(ctx context.Context, be core.Backend) error {
			chat, err := be.CreateGroup(ctx, strings.Join(args, " "))
			if err != nil {
				return err
			}
			return a.printCreated(chat)
		})
	}
	return c
}

func (a *app) downloadCmd() *command {
	c := a.newCommand("download <chat> <message-id>", "Download the photo or file of a message", 2, 2)
	dir := c.flags.StringP("output", "o", ".", "directory to save into")
	c.run = func(ctx context.Context, args []string) error {
		msgID, err := strconv.Atoi(args[1])
		if err != nil {
			return errUsage{fmt.Errorf("invalid message ID %q", args[1])}
		}
		return a.backend(ctx, false, func(ctx context.Context, be core.Backend) error {
			chat, err := be.Resolve(ctx, args[0])
			if err != nil {
				return err
			}
			path, err := be.Download(ctx, chat.ID, msgID, *dir)
			if err != nil {
				return err
			}
			return a.printPath(path)
		})
	}
	return c
}

func (a *app) watchCmd() *command {
	c := a.newCommand("watch [chat...]", "Print new messages as they arrive", 0, -1)
	c.long = `Print new and edited messages as they arrive, from every chat or only from
the given ones. Runs until interrupted, or until --count or --timeout is hit,
which is how an agent waits for a reply.

With --after, messages already newer than that ID are printed first, so
nothing is lost between sending or reading and starting to watch.`
	c.example = `  tg watch @alice --count 1 --timeout 10m
  tg watch @alice --after 4521 --count 1 --timeout 10m`
	count := c.flags.Int("count", 0, "stop after this many messages")
	timeout := c.flags.Duration("timeout", 0, "stop after this long, for example 90s or 10m")
	after := c.flags.Int("after", 0, "first print what is already newer than this message ID (one chat only)")
	outgoing := c.flags.Bool("outgoing", false, "also print messages sent from this account, by you or by anyone else using it")
	c.run = func(ctx context.Context, args []string) error {
		if *after > 0 && len(args) != 1 {
			return errUsage{errors.New("--after needs exactly one chat")}
		}
		if *timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, *timeout)
			defer cancel()
		}
		// skipped counts messages hidden for being sent from this account, so
		// a wait that ran out can say why it saw nothing.
		var seen, skipped atomic.Int64
		err := a.backend(ctx, true, func(ctx context.Context, be core.Backend) error {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			only := map[int64]core.Chat{}
			for _, ref := range args {
				chat, err := be.Resolve(ctx, ref)
				if err != nil {
					return err
				}
				only[chat.ID] = chat
			}

			var mu sync.Mutex
			newest := *after // highest message ID handled so far, used with --after
			show := func(ev core.Event) {
				mu.Lock()
				defer mu.Unlock()
				if _, wanted := only[ev.Chat.ID]; (len(only) > 0 && !wanted) || ctx.Err() != nil {
					return
				}
				if *after > 0 && ev.Kind == core.EventMessage {
					if ev.Message.ID <= newest {
						return // the backlog already covered it
					}
					newest = ev.Message.ID
				}
				if ev.Message.Out && !*outgoing {
					skipped.Add(1)
					return
				}
				if seen.Load() == 0 && !a.json {
					fmt.Println(untrusted)
				}
				a.printEvent(ev, len(only) != 1)
				if n := seen.Add(1); *count > 0 && n >= int64(*count) {
					cancel()
				}
			}
			return be.Watch(ctx, func(ev core.Event) {
				if ev.Kind != core.EventReady {
					show(ev)
					return
				}
				// The subscription is live and holds back new messages
				// until this returns: the moment to catch up.
				for _, chat := range only {
					if *after == 0 {
						break
					}
					backlog, err := be.History(ctx, chat.ID, core.HistoryQuery{AfterID: *after, Limit: 100})
					if err != nil {
						fmt.Fprintln(os.Stderr, "tg: could not load earlier messages:", err)
					}
					for _, m := range backlog {
						show(core.Event{Kind: core.EventMessage, Chat: chat, Message: m})
					}
				}
			})
		})
		// Running out of time is a normal way to stop, unless the caller was
		// waiting for a number of messages that did not all arrive.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			if n := seen.Load(); *count > 0 && n < int64(*count) {
				hint := ""
				if s := skipped.Load(); s > 0 {
					hint = fmt.Sprintf("; skipped %d sent from this account, add --outgoing to see them (agents sharing the account all count as you)", s)
				}
				return fmt.Errorf("timed out after %s with %d of %d messages%s", *timeout, n, *count, hint)
			}
			return nil
		}
		return err
	}
	return c
}

func (a *app) updateCmd() *command {
	c := a.newCommand("update", "Update tg to the latest release", 0, 0)
	c.run = func(ctx context.Context, _ []string) error {
		version, changed, err := selfupdate.Update(ctx, a.version)
		if err != nil {
			return err
		}
		if changed {
			fmt.Printf("updated tg %s -> %s\n", a.version, version)
		} else {
			fmt.Printf("tg %s is up to date\n", version)
		}
		return nil
	}
	return c
}
