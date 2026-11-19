package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/RealDoc06/tg-cli/internal/config"
	"github.com/RealDoc06/tg-cli/internal/core"
	"github.com/RealDoc06/tg-cli/internal/mtp"
)

// interactive reports whether a person is at the terminal. Tests replace it.
var interactive = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// needPerson refuses to loosen guardrails, or to remove an account, for a
// caller that is not a person at a terminal. Otherwise an agent told to by a
// message it read could lift its own restrictions with one command.
func needPerson(what string) error {
	if interactive() {
		return nil
	}
	return &core.RefusedError{Reason: what + " has to be done by a person at a terminal, it is not for agents. If you are one, ask your user"}
}

// loadPolicy reads the guardrails of an account from config.json.
func loadPolicy(account string) (core.Policy, error) {
	cfg, err := config.Load()
	if err != nil {
		return core.Policy{}, err
	}
	policy := core.Policy{Account: account}
	if entry := cfg.Accounts[account]; entry != nil {
		policy.ReadOnly = entry.ReadOnly
		for _, chat := range entry.SendTo {
			policy.WriteTo = append(policy.WriteTo, core.Chat{ID: chat.ID, Title: chat.Title})
		}
	}
	return policy, nil
}

// rememberUser keeps the owner shown by `tg accounts` current. It is best
// effort: the command at hand matters more than the label.
func rememberUser(cfg *config.File, account string, self core.User) {
	user := config.User{ID: self.ID, Name: self.Name, Username: self.Username}
	if entry := cfg.Accounts[account]; entry != nil && entry.User != nil && *entry.User == user {
		return
	}
	_ = config.Update(func(f *config.File) error {
		f.Ensure(account).User = &user
		if f.Account == "" {
			f.Account = account
		}
		return nil
	})
}

func describeUser(u *config.User) string {
	switch {
	case u == nil:
		return "not logged in"
	case u.Username != "":
		return u.Name + " @" + u.Username
	}
	return u.Name
}

func (a *app) accountsCmd() *command {
	c := a.newCommand("accounts [use|remove <name>]", "List the logged in accounts, or pick the default one", 0, 2)
	c.long = `List the accounts on this machine. Each has its own session and its own
guardrails. Add one with "tg login --account <name>".

  tg accounts                  list them, * marks the default
  tg accounts use <name>       make an account the default
  tg accounts remove <name>    log an account out and forget it

Any command takes --account <name> to act as another account once.`
	c.run = func(ctx context.Context, args []string) error {
		switch {
		case len(args) == 0:
			return a.printAccounts()
		case len(args) != 2:
			return errUsage{errors.New("usage: tg accounts [use|remove <name>]")}
		}
		name := args[1]
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if cfg.Accounts[name] == nil {
			return fmt.Errorf("no account %q, see `tg accounts`", name)
		}
		switch args[0] {
		case "use":
			// The default account is the identity every unqualified command
			// acts as, and its guardrails may be looser.
			if err := needPerson("changing the default account"); err != nil {
				return err
			}
			if err := config.Update(func(f *config.File) error { f.Account = name; return nil }); err != nil {
				return err
			}
			return a.printAccounts()
		case "remove":
			return a.logout(ctx, name)
		}
		return errUsage{fmt.Errorf("unknown action %q, use `use` or `remove`", args[0])}
	}
	return c
}

// logout ends an account's session and removes it, guardrails included, from
// the configuration.
func (a *app) logout(ctx context.Context, name string) error {
	if err := needPerson("logging out"); err != nil {
		return err
	}
	if os.Getenv("TG_DEMO") == "" {
		err := mtp.Logout(ctx, mtp.Options{Account: name, Version: a.version})
		if err != nil && !errors.Is(err, core.ErrNotLoggedIn) {
			return err
		}
	}
	err := config.Update(func(f *config.File) error {
		delete(f.Accounts, name)
		if f.Account == name {
			f.Account = ""
			if rest := f.Names(); len(rest) > 0 {
				f.Account = rest[0]
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Logged out of account %q\n", name)
	return nil
}

func (a *app) printAccounts() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	current := cfg.Default()
	if a.json {
		for _, name := range cfg.Names() {
			entry := cfg.Accounts[name]
			err := emitJSON(map[string]any{
				"account": name, "default": name == current, "user": entry.User,
				"read_only": entry.ReadOnly, "send_to": entry.SendTo,
			})
			if err != nil {
				return err
			}
		}
		return nil
	}
	if len(cfg.Accounts) == 0 {
		fmt.Println("no accounts, run `tg login`")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  ACCOUNT\tUSER\tLIMITS")
	for _, name := range cfg.Names() {
		entry := cfg.Accounts[name]
		mark := " "
		if name == current {
			mark = "*"
		}
		limits := "none"
		switch {
		case entry.ReadOnly:
			limits = "read-only"
		case len(entry.SendTo) > 0:
			limits = fmt.Sprintf("sends to %d chats only", len(entry.SendTo))
		}
		fmt.Fprintf(w, "%s %s\t%s\t%s\n", mark, name, describeUser(entry.User), limits)
	}
	return w.Flush()
}

func (a *app) configCmd() *command {
	c := a.newCommand("config [read-only on|off] [allow|disallow <chat>...]", "Show or change an account's guardrails", 0, -1)
	c.long = `Show or change what an account is allowed to do. The settings live in
config.json, which you can also edit by hand: the commands and the file are
the same thing.

  tg config                     show the guardrails and where the file is
  tg config read-only on|off    forbid everything that changes the account
  tg config allow <chat>...     write only to these chats (adds to the list)
  tg config disallow <chat>...  take chats off the list
  tg config allow --any         drop the list, any chat can be written to

Guardrails apply to every write: sending, marking as read, creating groups.
Reading is never limited. Making them looser needs a person at a terminal:
turning read-only off, adding to a list or dropping it. Tightening does not.`
	lift := c.flags.Bool("any", false, "with allow: lift the limit, any chat can be written to")
	c.run = func(ctx context.Context, args []string) error {
		account, _, err := a.active()
		if err != nil {
			return err
		}
		switch {
		case len(args) == 0:
			return a.printConfig(account)

		case args[0] == "read-only" && len(args) == 2 && (args[1] == "on" || args[1] == "off"):
			on := args[1] == "on"
			if !on {
				if err := needPerson("turning read-only off"); err != nil {
					return err
				}
			}
			err = config.Update(func(f *config.File) error { f.Ensure(account).ReadOnly = on; return nil })

		case args[0] == "allow" && (*lift != (len(args) > 1)):
			var chats []config.Chat
			if !*lift {
				if chats, err = a.resolveAll(ctx, args[1:]); err != nil {
					return err
				}
			}
			err = config.Update(func(f *config.File) error {
				entry := f.Ensure(account)
				// Starting a list narrows an open account. Adding to one,
				// or dropping it, widens what can be written to.
				if len(entry.SendTo) > 0 || *lift {
					if err := needPerson("allowing more chats"); err != nil {
						return err
					}
				}
				if *lift {
					entry.SendTo = nil
				}
				for _, chat := range chats {
					if !slices.ContainsFunc(entry.SendTo, func(c config.Chat) bool { return c.ID == chat.ID }) {
						entry.SendTo = append(entry.SendTo, chat)
					}
				}
				return nil
			})

		case args[0] == "disallow" && len(args) > 1:
			var ids []int64
			if ids, err = a.listedIDs(ctx, account, args[1:]); err != nil {
				return err
			}
			err = config.Update(func(f *config.File) error { return disallow(f.Ensure(account), ids) })

		default:
			return errUsage{errors.New("usage: tg config [read-only on|off] [allow|disallow <chat>...], see `tg config --help`")}
		}
		if err != nil {
			return err
		}
		return a.printConfig(account)
	}
	return c
}

// resolveAll turns chat references into allowlist entries.
func (a *app) resolveAll(ctx context.Context, refs []string) ([]config.Chat, error) {
	var chats []config.Chat
	err := a.backend(ctx, false, func(ctx context.Context, be core.Backend) error {
		for _, ref := range refs {
			chat, err := be.Resolve(ctx, ref)
			if err != nil {
				return err
			}
			chats = append(chats, config.Chat{ID: chat.ID, Title: chat.Title})
		}
		return nil
	})
	return chats, err
}

// listedIDs turns the references given to `config disallow` into chat IDs.
// IDs and the titles stored in the list need no connection, anything else
// (a @username) is resolved like `config allow` does.
func (a *app) listedIDs(ctx context.Context, account string, refs []string) ([]int64, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	var listed []config.Chat
	if entry := cfg.Accounts[account]; entry != nil {
		listed = entry.SendTo
	}
	ids := make([]int64, 0, len(refs))
	for _, ref := range refs {
		if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
			ids = append(ids, id)
			continue
		}
		if i := slices.IndexFunc(listed, func(c config.Chat) bool { return strings.EqualFold(c.Title, ref) }); i >= 0 {
			ids = append(ids, listed[i].ID)
			continue
		}
		resolved, err := a.resolveAll(ctx, []string{ref})
		if err != nil {
			return nil, err
		}
		ids = append(ids, resolved[0].ID)
	}
	return ids, nil
}

// disallow takes chats off an account's allowlist. It never removes the last
// one: an empty list means no limit, which would turn a tightening command
// into a loosening one.
func disallow(entry *config.Account, ids []int64) error {
	left := slices.Clone(entry.SendTo)
	for _, id := range ids {
		i := slices.IndexFunc(left, func(c config.Chat) bool { return c.ID == id })
		if i < 0 {
			return fmt.Errorf("chat %d is not on the list, see `tg config`", id)
		}
		left = slices.Delete(left, i, i+1)
	}
	if len(left) == 0 {
		return errors.New("that would empty the list and allow every chat. " +
			"To stop all writing use `tg config read-only on`, to lift the limit use `tg config allow --any`")
	}
	entry.SendTo = left
	return nil
}

func (a *app) printConfig(account string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	entry := cfg.Accounts[account]
	if entry == nil {
		entry = &config.Account{}
	}
	if a.json {
		return emitJSON(map[string]any{
			"file": config.FilePath(), "account": account, "user": entry.User,
			"read_only": entry.ReadOnly, "send_to": append([]config.Chat{}, entry.SendTo...),
		})
	}
	readOnly := "off"
	if entry.ReadOnly {
		readOnly = "on, nothing can be sent or changed"
	}
	fmt.Printf("file       %s\n", config.FilePath())
	fmt.Printf("account    %s (%s)\n", account, describeUser(entry.User))
	fmt.Printf("read-only  %s\n", readOnly)
	if len(entry.SendTo) == 0 {
		fmt.Println("sends to   any chat")
		return nil
	}
	fmt.Println("sends to   only these chats:")
	for _, chat := range entry.SendTo {
		fmt.Printf("           %d  %s\n", chat.ID, chat.Title)
	}
	return nil
}
