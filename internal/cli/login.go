package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/RealDoc06/tg-cli/internal/config"
	"github.com/RealDoc06/tg-cli/internal/core"
	"github.com/RealDoc06/tg-cli/internal/demo"
	"github.com/RealDoc06/tg-cli/internal/mtp"
)

// prompter asks for login details on the terminal, unless a flag already
// answered. It implements mtp.Prompter.
type prompter struct {
	phone, code, password string
	stdin                 *bufio.Reader
}

// ask returns preset, or prompts for a line. The read happens on the side so
// that ctrl+c still ends the program while it waits.
func (p *prompter) ask(ctx context.Context, label, preset string) (string, error) {
	if preset != "" {
		return preset, nil
	}
	fmt.Fprint(os.Stderr, label+": ")
	type answer struct {
		line string
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		line, err := p.stdin.ReadString('\n')
		done <- answer{line, err}
	}()
	select {
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr)
		return "", ctx.Err()
	case a := <-done:
		if a.err != nil && a.line == "" {
			return "", fmt.Errorf("no answer for %q: %w", label, a.err)
		}
		return strings.TrimSpace(a.line), nil
	}
}

func (p *prompter) Phone(ctx context.Context) (string, error) {
	return p.ask(ctx, "Phone number (international format, like +15551234567)", p.phone)
}

func (p *prompter) Code(ctx context.Context) (string, error) {
	return p.ask(ctx, "Login code (sent to your Telegram app or by SMS)", p.code)
}

// Password asks for the two-step verification password without echoing it.
func (p *prompter) Password(ctx context.Context) (string, error) {
	if p.password != "" {
		return p.password, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return p.ask(ctx, "Two-step verification password", "")
	}
	fmt.Fprint(os.Stderr, "Two-step verification password: ")
	secret, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(secret), err
}

func (a *app) loginCmd() *command {
	c := a.newCommand("login", "Log in to a Telegram account", 0, 0)
	c.long = `Log in with a phone number and the code Telegram sends to it. The session is
stored on this machine only and stays valid until "tg logout".

To add another account next to the first, give it a name:
  tg login --account work

Telegram requires every client to identify itself with an API ID and hash.
If this build has none, create them once at https://my.telegram.org/apps
and tg will ask for them here.`
	p := &prompter{stdin: bufio.NewReader(os.Stdin)}
	c.flags.StringVar(&p.phone, "phone", "", "phone number, instead of asking")
	c.flags.StringVar(&p.code, "code", "", "login code, instead of asking")
	c.flags.StringVar(&p.password, "password", "", "two-step verification password, instead of asking")
	apiID := c.flags.String("api-id", "", "Telegram API ID to save")
	apiHash := c.flags.String("api-hash", "", "Telegram API hash to save")
	c.run = func(ctx context.Context, _ []string) error {
		account, _, err := a.active()
		if err != nil {
			return err
		}
		if os.Getenv("TG_DEMO") != "" {
			// The demo has nothing to log in to, but registering the account
			// lets the account commands be tried.
			return a.loggedIn(account, demo.New().Self())
		}
		if *apiID != "" || *apiHash != "" {
			// Parsed here rather than by the flag, so a hash pasted in the
			// wrong place is not echoed back in an error message.
			id, err := strconv.Atoi(*apiID)
			if err != nil || *apiHash == "" {
				return errUsage{errors.New("--api-id takes the numeric ID and needs --api-hash with it")}
			}
			if err := (config.Credentials{APIID: id, APIHash: *apiHash}).Save(); err != nil {
				return err
			}
		}
		if _, err := config.LoadCredentials(); errors.Is(err, config.ErrNoCredentials) && os.Getenv("TG_TEST_DC") == "" {
			fmt.Fprintln(os.Stderr, "Telegram API credentials are needed once. Create them at https://my.telegram.org/apps")
			id, err := p.ask(ctx, "API ID", "")
			if err != nil {
				return err
			}
			n, err := strconv.Atoi(id)
			if err != nil {
				return errors.New("the API ID is the short number, not the hash")
			}
			hash, err := p.ask(ctx, "API hash", "")
			if err != nil {
				return err
			}
			if err := (config.Credentials{APIID: n, APIHash: hash}).Save(); err != nil {
				return err
			}
		}
		user, err := mtp.Login(ctx, mtp.Options{Account: account, Version: a.version}, p)
		if err != nil {
			return err
		}
		return a.loggedIn(account, user)
	}
	return c
}

// loggedIn records a fresh login in the configuration and reports it. The
// first account becomes the default, later ones never take that over.
func (a *app) loggedIn(account string, user core.User) error {
	var current string
	err := config.Update(func(f *config.File) error {
		f.Ensure(account).User = &config.User{ID: user.ID, Name: user.Name, Username: user.Username}
		if f.Account == "" {
			f.Account = account
		}
		current = f.Account
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Logged in as %s (account %q)\n", user.Name, account)
	if current != account {
		fmt.Fprintf(os.Stderr, "The default account is still %q. Use this one with --account %s, or run `tg accounts use %s`.\n", current, account, account)
	}
	return a.printUser(user)
}

func (a *app) logoutCmd() *command {
	c := a.newCommand("logout", "End the session and delete it from this machine", 0, 0)
	c.run = func(ctx context.Context, _ []string) error {
		account, cfg, err := a.active()
		if err != nil {
			return err
		}
		if cfg.Accounts[account] == nil {
			return core.ErrNotLoggedIn
		}
		return a.logout(ctx, account)
	}
	return c
}
