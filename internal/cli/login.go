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

Telegram requires every client to identify itself with an API ID and hash.
If this build has none, create them once at https://my.telegram.org/apps
and tg will ask for them here.`
	p := &prompter{stdin: bufio.NewReader(os.Stdin)}
	c.flags.StringVar(&p.phone, "phone", "", "phone number, instead of asking")
	c.flags.StringVar(&p.code, "code", "", "login code, instead of asking")
	c.flags.StringVar(&p.password, "password", "", "two-step verification password, instead of asking")
	apiID := c.flags.Int("api-id", 0, "Telegram API ID to save")
	apiHash := c.flags.String("api-hash", "", "Telegram API hash to save")
	c.run = func(ctx context.Context, _ []string) error {
		if os.Getenv("TG_DEMO") != "" {
			return errors.New("TG_DEMO is set, there is nothing to log in to")
		}
		if *apiID != 0 && *apiHash != "" {
			if err := (config.Credentials{APIID: *apiID, APIHash: *apiHash}).Save(); err != nil {
				return err
			}
		}
		if _, err := config.LoadCredentials(); err != nil && os.Getenv("TG_TEST_DC") == "" {
			fmt.Fprintln(os.Stderr, "Telegram API credentials are needed once. Create them at https://my.telegram.org/apps")
			id, err := p.ask(ctx, "API ID", "")
			if err != nil {
				return err
			}
			n, err := strconv.Atoi(id)
			if err != nil {
				return fmt.Errorf("the API ID is a number, got %q", id)
			}
			hash, err := p.ask(ctx, "API hash", "")
			if err != nil {
				return err
			}
			if err := (config.Credentials{APIID: n, APIHash: hash}).Save(); err != nil {
				return err
			}
		}
		user, err := mtp.Login(ctx, mtp.Options{Version: a.version}, p)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Logged in as %s\n", user.Name)
		return a.printUser(user)
	}
	return c
}

func (a *app) logoutCmd() *command {
	c := a.newCommand("logout", "End the session and delete it from this machine", 0, 0)
	c.run = func(ctx context.Context, _ []string) error {
		if err := mtp.Logout(ctx, mtp.Options{Version: a.version}); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Logged out")
		return nil
	}
	return c
}
