// Package mtp is the Telegram backend: it speaks MTProto as a user account
// through gotd and exposes it as a core.Backend.
package mtp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/telegram/updates/hook"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/RealDoc06/tg-cli/internal/config"
	"github.com/RealDoc06/tg-cli/internal/core"
)

// maxFloodWait is the longest rate limit pause that is waited out silently.
// Longer ones are returned so an agent is never blocked for minutes.
const maxFloodWait = 30 * time.Second

// Options configure a connection.
type Options struct {
	// Account names the account whose session is used.
	Account string
	// Version is reported to Telegram and shown in its list of sessions.
	Version string
	// Updates enables live updates, required for Backend.Watch.
	Updates bool
}

// Prompter asks the user for what a login needs.
type Prompter interface {
	Phone(ctx context.Context) (string, error)
	Code(ctx context.Context) (string, error)
	Password(ctx context.Context) (string, error)
}

// testDC switches to Telegram's test servers, for development only.
func testDC() bool { return os.Getenv("TG_TEST_DC") != "" }

func sessionPath(account string) string {
	return filepath.Join(config.AccountDir(account), "session.json")
}

func peersPath(account string) string {
	return filepath.Join(config.AccountDir(account), "peers.json")
}

// newClient builds a client and, when updates are requested, the manager
// that keeps them ordered.
func newClient(opts Options) (*telegram.Client, *updates.Manager, tg.UpdateDispatcher, error) {
	dispatcher := tg.NewUpdateDispatcher()
	creds, err := config.LoadCredentials()
	if testDC() {
		creds, err = config.Credentials{APIID: telegram.TestAppID, APIHash: telegram.TestAppHash}, nil
	}
	if err != nil {
		return nil, nil, dispatcher, err
	}
	if err := os.MkdirAll(config.AccountDir(opts.Account), 0o700); err != nil {
		return nil, nil, dispatcher, err
	}

	waitFlood := telegram.MiddlewareFunc(func(next tg.Invoker) telegram.InvokeFunc {
		return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
			for {
				err := next.Invoke(ctx, input, output)
				if wait, ok := tgerr.AsFloodWait(err); !ok || wait > maxFloodWait {
					return err
				}
				if _, err := tgerr.FloodWait(ctx, err); err != nil {
					return err
				}
			}
		}
	})

	tgOpts := telegram.Options{
		SessionStorage: &session.FileStorage{Path: sessionPath(opts.Account)},
		Middlewares:    []telegram.Middleware{waitFlood},
		NoUpdates:      !opts.Updates,
		Device: telegram.DeviceConfig{
			DeviceModel:   "tg-cli",
			SystemVersion: runtime.GOOS + "/" + runtime.GOARCH,
			AppVersion:    opts.Version,
		},
	}
	if testDC() {
		tgOpts.DCList = dcs.Test()
	}

	var gaps *updates.Manager
	if opts.Updates {
		// Updates that arrive before Watch runs are held back and replayed
		// after its EventReady, never handed out ahead of it.
		gaps = updates.New(updates.Config{Handler: dispatcher, BeforeRun: updates.BeforeRunBuffer})
		tgOpts.UpdateHandler = gaps
		// Feed the results of our own requests to the manager too, so a sent
		// message does not look like a gap in the update sequence.
		tgOpts.Middlewares = append(tgOpts.Middlewares, hook.UpdateHook(gaps.Handle))
	}
	return telegram.NewClient(creds.APIID, creds.APIHash, tgOpts), gaps, dispatcher, nil
}

// Run connects with the saved session and calls fn with a ready backend.
func Run(ctx context.Context, opts Options, fn func(context.Context, core.Backend) error) error {
	if _, err := os.Stat(sessionPath(opts.Account)); err != nil {
		return core.ErrNotLoggedIn
	}
	client, gaps, dispatcher, err := newClient(opts)
	if err != nil {
		return err
	}

	err = runClient(ctx, client, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if !status.Authorized {
			return core.ErrNotLoggedIn
		}
		b := &backend{
			client: client, api: client.API(), self: selfUser(status.User),
			peers: loadPeers(peersPath(opts.Account)), sender: newSender(client.API()),
			gaps: gaps, imageSlots: make(chan struct{}, 4),
		}
		b.peers.learn(tg.Entities{Users: map[int64]*tg.User{status.User.ID: status.User}}, b.self.ID)
		dispatcher.OnAnyNewMessage(func(_ context.Context, e tg.Entities, msg tg.MessageClass) error {
			b.emit(core.EventMessage, e, msg)
			return nil
		})
		dispatcher.OnAnyEditMessage(func(_ context.Context, e tg.Entities, msg tg.MessageClass) error {
			b.emit(core.EventEdit, e, msg)
			return nil
		})

		err = fn(ctx, b)
		if saveErr := b.peers.save(); err == nil {
			err = saveErr
		}
		return err
	})
	if auth.IsUnauthorized(err) {
		return core.ErrNotLoggedIn
	}
	return friendly(err)
}

// runClient connects, runs fn and disconnects. Once fn has run its error is
// returned as is, without the layers the client wraps around it.
func runClient(ctx context.Context, client *telegram.Client, fn func(context.Context) error) error {
	var fnErr error
	called := false
	err := client.Run(ctx, func(ctx context.Context) error {
		called = true
		fnErr = fn(ctx)
		return fnErr
	})
	if called {
		return fnErr
	}
	return err
}

// friendly replaces the wrapped RPC error chain with one readable sentence.
func friendly(err error) error {
	if wait, ok := tgerr.AsFloodWait(err); ok {
		return fmt.Errorf("rate limited by Telegram, retry in %s", wait.Round(time.Second))
	}
	rpc, ok := tgerr.As(err)
	if !ok {
		return err
	}
	switch rpc.Type {
	case "PHONE_CODE_INVALID", "PHONE_CODE_EMPTY":
		return errors.New("wrong login code")
	case "PHONE_CODE_EXPIRED":
		return errors.New("the login code expired, run `tg login` again")
	case "PHONE_NUMBER_INVALID":
		return errors.New("invalid phone number, use the international format like +15551234567")
	case "PASSWORD_HASH_INVALID":
		return errors.New("wrong two-step verification password")
	case "API_ID_INVALID", "API_ID_PUBLISHED_FLOOD":
		return errors.New("Telegram rejected the API ID and hash, check them at https://my.telegram.org/apps")
	}
	return fmt.Errorf("Telegram refused the request: %s (%d)", rpc.Type, rpc.Code)
}

// Login authorizes this installation and returns the account.
func Login(ctx context.Context, opts Options, prompt Prompter) (core.User, error) {
	client, _, _, err := newClient(opts)
	if err != nil {
		return core.User{}, err
	}
	var user core.User
	err = runClient(ctx, client, func(ctx context.Context) error {
		flow := auth.NewFlow(authenticator{prompt}, auth.SendCodeOptions{})
		if err := client.Auth().IfNecessary(ctx, flow); err != nil {
			return err
		}
		self, err := client.Self(ctx)
		if err != nil {
			return err
		}
		user = selfUser(self)
		return nil
	})
	return user, friendly(err)
}

// Logout ends the session on Telegram's side and deletes the account's
// files locally.
func Logout(ctx context.Context, opts Options) error {
	if _, err := os.Stat(sessionPath(opts.Account)); err != nil {
		return core.ErrNotLoggedIn
	}
	client, _, _, err := newClient(opts)
	if err != nil {
		return err
	}
	err = runClient(ctx, client, func(ctx context.Context) error {
		_, err := client.LogOut(ctx)
		return err
	})
	if err != nil && !auth.IsUnauthorized(err) {
		return err
	}
	return os.RemoveAll(config.AccountDir(opts.Account))
}

func selfUser(u *tg.User) core.User {
	return core.User{ID: u.ID, Name: userName(u), Username: u.Username, Phone: u.Phone}
}

// authenticator adapts a Prompter to gotd's login flow.
type authenticator struct{ prompt Prompter }

func (a authenticator) Phone(ctx context.Context) (string, error) { return a.prompt.Phone(ctx) }
func (a authenticator) Password(ctx context.Context) (string, error) {
	return a.prompt.Password(ctx)
}
func (a authenticator) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	return a.prompt.Code(ctx)
}

func (a authenticator) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return nil
}

func (a authenticator) SignUp(context.Context) (auth.UserInfo, error) {
	if testDC() {
		return auth.UserInfo{FirstName: "Test", LastName: "User"}, nil
	}
	return auth.UserInfo{}, errors.New("this number has no Telegram account, create one with an official app first")
}
