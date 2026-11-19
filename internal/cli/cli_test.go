package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RealDoc06/tg-cli/internal/config"
	"github.com/RealDoc06/tg-cli/internal/core"
)

// run dispatches a command line against the demo data and discards what it
// prints. Every call within one test shares a fresh home directory.
func run(t *testing.T, args ...string) error {
	t.Helper()
	_, err := output(t, args...)
	return err
}

// output is run, returning what the command printed.
func output(t *testing.T, args ...string) (string, error) {
	t.Helper()
	if os.Getenv("TG_TEST_OWNER") != t.Name() {
		t.Setenv("TG_TEST_OWNER", t.Name())
		t.Setenv("TG_CLI_HOME", t.TempDir())
		t.Setenv("TG_DEMO", "1")
		t.Setenv("TG_NO_AUTOUPDATE", "1")
		t.Setenv("TG_ACCOUNT", "")
	}
	var runErr error
	printed := captured(t, func() {
		runErr = (&app{version: "dev"}).dispatch(context.Background(), args)
	})
	return printed, runErr
}

// captured returns what fn printed to stdout.
func captured(t *testing.T, fn func()) string {
	t.Helper()
	capture, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	stdout := os.Stdout
	os.Stdout = capture
	defer func() { os.Stdout = stdout }()
	fn()
	printed, err := os.ReadFile(capture.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(printed)
}

// writeConfig puts content in config.json, the way a person with an editor
// would.
func writeConfig(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.FilePath(), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// atTerminal makes the commands believe a person is, or is not, typing.
func atTerminal(t *testing.T, person bool) {
	t.Helper()
	previous := interactive
	interactive = func() bool { return person }
	t.Cleanup(func() { interactive = previous })
}

// Group and channel IDs start with a dash and must not be taken for flags,
// wherever they sit on the line.
func TestNegativeChatIDsAreArguments(t *testing.T) {
	for _, args := range [][]string{
		{"read", "-1001000000201", "-n", "2"},
		{"read", "-n", "2", "-202"},
		{"send", "-202", "hello"},
		{"search", "backups", "-c", "-1001000000201"},
		{"search", "backups", "--chat", "-1001000000201", "--json"},
	} {
		if err := run(t, args...); err != nil {
			t.Errorf("tg %s: %v", strings.Join(args, " "), err)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"read"},
		{"read", "alice", "--bogus"},
		{"nope"},
		{"send", "alice"}, // no text, stdin is not a pipe with content
	} {
		if err := run(t, args...); !errors.As(err, new(errUsage)) {
			t.Errorf("tg %s: got %v, want a usage error", strings.Join(args, " "), err)
		}
	}
	// A wrong flag value must be reported without the shield byte leaking.
	if err := run(t, "read", "alice", "-n", "-5x"); err == nil || strings.Contains(err.Error(), shield) {
		t.Errorf("bad flag value: %q", err)
	}
}

func TestMessageLine(t *testing.T) {
	date := time.Date(2026, 3, 4, 9, 5, 0, 0, time.UTC)
	cases := map[string]core.Message{
		"[7] 2026-03-04 09:05 Ann: hi": {ID: 7, From: "Ann", Date: date, Text: "hi"},
		"[8] 2026-03-04 09:05 me (reply to 7, edited): [photo] look\n  second line": {
			ID: 8, From: "Sam", Out: true, Date: date, Text: "look\nsecond line", Media: "photo", ReplyTo: 7, Edited: true,
		},
		"[9] 2026-03-04 09:05 Ann in Team (-5): yo": {ID: 9, From: "Ann", Date: date, Text: "yo", Chat: "Team", ChatID: -5},
	}
	for want, msg := range cases {
		if got := messageLine(msg); got != want {
			t.Errorf("messageLine = %q, want %q", got, want)
		}
	}
}

// watch --after must hand over what arrived before it started, or an agent
// that reads and then waits loses the messages in between.
func TestWatchAfterCatchesUp(t *testing.T) {
	// Alice's demo messages 4 and 5 are newer than 3: both count, no waiting.
	if err := run(t, "watch", "alice", "--after", "3", "--count", "2", "--timeout", "5s"); err != nil {
		t.Errorf("catching up on two messages: %v", err)
	}
	// Nothing is newer than 5, so the wait has to run out.
	err := run(t, "watch", "alice", "--after", "5", "--count", "1", "--timeout", "200ms")
	if err == nil || !strings.Contains(err.Error(), "0 of 1") {
		t.Errorf("waiting past the newest message: %v", err)
	}
	for _, args := range [][]string{{"watch", "--after", "3"}, {"watch", "--after=-1"}} {
		if err := run(t, args...); !errors.As(err, new(errUsage)) {
			t.Errorf("tg %s: %v", strings.Join(args, " "), err)
		}
	}
}

func refused(err error) bool { return errors.As(err, new(*core.RefusedError)) }

// An agent has no terminal: it may tighten the guardrails but never loosen
// them, or a message it read could talk it out of its own limits.
func TestReadOnlyNeedsAPersonToLift(t *testing.T) {
	atTerminal(t, false)
	if err := run(t, "config", "read-only", "on"); err != nil {
		t.Fatalf("turning read-only on: %v", err)
	}
	for _, write := range [][]string{{"send", "alice", "hi"}, {"mark-read", "alice"}, {"create-group", "x"}, {"read", "alice", "--mark-read"}} {
		if err := run(t, write...); !refused(err) {
			t.Errorf("tg %s on a read-only account: %v", strings.Join(write, " "), err)
		}
	}
	if err := run(t, "read", "alice"); err != nil {
		t.Errorf("reading on a read-only account: %v", err)
	}
	if err := run(t, "config", "read-only", "off"); !refused(err) {
		t.Fatalf("lifting read-only without a terminal: %v", err)
	}
	if err := run(t, "send", "alice", "hi"); !refused(err) {
		t.Error("the refused command lifted read-only anyway")
	}

	atTerminal(t, true)
	if err := run(t, "config", "read-only", "off"); err != nil {
		t.Fatalf("lifting read-only at a terminal: %v", err)
	}
	if err := run(t, "send", "alice", "hi"); err != nil {
		t.Errorf("sending after read-only was lifted: %v", err)
	}
}

func TestAllowlist(t *testing.T) {
	// Starting a list narrows an open account, so an agent may do it.
	// Growing the list is what needs a person.
	atTerminal(t, false)
	if err := run(t, "config", "allow", "alice"); err != nil {
		t.Fatalf("starting an allowlist without a terminal: %v", err)
	}
	if err := run(t, "config", "allow", "-202"); !refused(err) {
		t.Fatalf("growing the allowlist without a terminal: %v", err)
	}
	if err := run(t, "config", "allow", "--any"); !refused(err) {
		t.Fatalf("dropping the allowlist without a terminal: %v", err)
	}
	atTerminal(t, true)
	if err := run(t, "config", "allow", "-202", "@bobchen"); err != nil {
		t.Fatal(err)
	}
	atTerminal(t, false)
	if err := run(t, "config", "disallow", "@bobchen"); err != nil {
		t.Errorf("removing a chat by username: %v", err)
	}

	if err := run(t, "send", "alice", "hi"); err != nil {
		t.Errorf("sending to a listed chat: %v", err)
	}
	if err := run(t, "send", "@bobchen", "hi"); !refused(err) {
		t.Errorf("sending to an unlisted chat: %v", err)
	}
	if err := run(t, "create-group", "x"); !refused(err) {
		t.Errorf("creating a group under an allowlist: %v", err)
	}

	// Taking chats off is tightening, except the last one: an empty list
	// would mean no limit at all.
	if err := run(t, "config", "disallow", "Family"); err != nil {
		t.Errorf("removing a chat by title: %v", err)
	}
	if err := run(t, "send", "-202", "hi"); !refused(err) {
		t.Errorf("sending to a chat taken off the list: %v", err)
	}
	if err := run(t, "config", "disallow", "101"); err == nil {
		t.Error("the last chat was removed, lifting the limit")
	}
	if err := run(t, "send", "@bobchen", "hi"); !refused(err) {
		t.Error("the limit is gone")
	}
}

// The file and the commands are one thing: what a person writes in
// config.json is enforced and shown, and what a command changes lands in it
// without losing the rest.
func TestConfigFileAndCommandsAgree(t *testing.T) {
	atTerminal(t, true)
	if err := run(t, "config"); err != nil { // sets up the home directory
		t.Fatal(err)
	}
	byHand := `{"api_id": 7, "api_hash": "h", "accounts": {"default": {"read_only": false, "send_to": [{"id": 101}]}}}`
	writeConfig(t, byHand)

	if err := run(t, "send", "@bobchen", "hi"); !refused(err) {
		t.Errorf("a hand written allowlist is not enforced: %v", err)
	}
	shown, err := output(t, "config", "--json")
	if err != nil || !strings.Contains(shown, `"id":101`) {
		t.Errorf("tg config does not show the hand written list: %q, %v", shown, err)
	}

	if err := run(t, "config", "allow", "-202"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	list := cfg.Accounts["default"].SendTo
	if len(list) != 2 || list[0].ID != 101 || list[1].ID != -202 || list[1].Title != "Family" {
		t.Errorf("list in the file after the command: %+v", list)
	}
	if cfg.APIID != 7 || cfg.APIHash != "h" {
		t.Errorf("the command dropped other settings: %+v", cfg)
	}

	// A misspelled guardrail must stop everything rather than be ignored.
	writeConfig(t, `{"accounts": {"default": {"readonly": true}}}`)
	if err := run(t, "send", "alice", "hi"); err == nil || !strings.Contains(err.Error(), "readonly") {
		t.Errorf("a misspelled key was accepted: %v", err)
	}
}

func TestAccounts(t *testing.T) {
	atTerminal(t, true)
	for _, login := range [][]string{{"login"}, {"login", "--account", "work"}} {
		if err := run(t, login...); err != nil {
			t.Fatal(err)
		}
	}
	// The mark shows the configured default, whatever this one call acts as.
	for _, args := range [][]string{{"accounts", "--json"}, {"accounts", "--json", "--account", "work"}} {
		listed, err := output(t, args...)
		if err != nil || strings.Count(listed, "\n") != 2 || !strings.Contains(listed, `"account":"default","default":true`) {
			t.Fatalf("tg %s: %q, %v", strings.Join(args, " "), listed, err)
		}
	}

	// Guardrails belong to one account.
	if err := run(t, "config", "read-only", "on", "--account", "work"); err != nil {
		t.Fatal(err)
	}
	if err := run(t, "send", "alice", "hi", "--account", "work"); !refused(err) {
		t.Errorf("sending as the read-only account: %v", err)
	}
	if err := run(t, "send", "alice", "hi"); err != nil {
		t.Errorf("sending as the other account: %v", err)
	}

	if err := run(t, "chats", "--account", "nope"); err == nil {
		t.Error("an unknown account was accepted")
	}
	if err := run(t, "chats", "--account", "../escape"); !errors.As(err, new(errUsage)) {
		t.Errorf("an unsafe account name: %v", err)
	}

	atTerminal(t, false)
	if err := run(t, "accounts", "use", "work"); !refused(err) {
		t.Errorf("switching the default account without a terminal: %v", err)
	}
	atTerminal(t, true)
	if err := run(t, "accounts", "use", "work"); err != nil {
		t.Fatal(err)
	}
	if err := run(t, "send", "alice", "hi"); !refused(err) {
		t.Errorf("the default account did not change: %v", err)
	}

	// Removing an account drops its guardrails with it: not for agents.
	atTerminal(t, false)
	for _, args := range [][]string{{"accounts", "remove", "work"}, {"logout"}} {
		if err := run(t, args...); !refused(err) {
			t.Errorf("tg %s without a terminal: %v", strings.Join(args, " "), err)
		}
	}
	atTerminal(t, true)
	if err := run(t, "accounts", "remove", "work"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.Load(); cfg.Account != "default" || cfg.Accounts["work"] != nil {
		t.Errorf("after removing the default account: %+v", cfg)
	}

	// The same when the file names no default: "default" is it, and
	// removing it must hand over to an account that exists.
	writeConfig(t, `{"accounts": {"default": {}, "work": {}}}`)
	if err := run(t, "logout"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.Load(); cfg.Default() != "work" {
		t.Errorf("default after removing the implicit one: %q", cfg.Default())
	}
}

// Login flags carry the code and the password. They must stay out of the
// log wherever the command sits on the line.
func TestLoginArgumentsAreNotLogged(t *testing.T) {
	t.Setenv("TG_CLI_HOME", t.TempDir())
	t.Setenv("TG_DEMO", "")
	// A configuration that cannot be read makes dispatch fail early, and a
	// bad flag before the command keeps it from being recognized at all.
	writeConfig(t, "{broken")
	if err := os.WriteFile(config.Path("session.json"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--json", "login", "--code", "13579", "--password", "hunter2"},
		{"--bogus", "login", "--code", "13579", "--password", "hunter2"},
	} {
		a := &app{version: "dev"}
		err := a.dispatch(context.Background(), args)
		if err == nil {
			t.Fatalf("tg %s was expected to fail", strings.Join(args, " "))
		}
		logInvocation(a.acting, a.ran, args, time.Now(), 1, errors.New("failed with --password hunter2"))
	}

	logged, err := os.ReadFile(config.Path("log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logged), "13579") || strings.Contains(string(logged), "hunter2") || strings.Count(string(logged), `["login"]`) != 2 {
		t.Errorf("log holds: %s", logged)
	}
}

// scripted is a backend that replays a fixed conversation to a watcher.
type scripted struct {
	core.Backend
	backlog []core.Message
	live    []core.Event
}

func (s scripted) Resolve(context.Context, string) (core.Chat, error) {
	return core.Chat{ID: 1, Title: "Team"}, nil
}

func (s scripted) History(context.Context, int64, core.HistoryQuery) ([]core.Message, error) {
	return s.backlog, nil
}

func (s scripted) Watch(ctx context.Context, fn func(core.Event)) error {
	fn(core.Event{Kind: core.EventReady})
	for _, ev := range s.live {
		fn(ev)
	}
	<-ctx.Done()
	return nil
}

// Between the backlog and the live stream nothing may be printed twice, out
// of order, or counted when it is not a new message.
func TestWatchJoinsBacklogAndLiveStream(t *testing.T) {
	chat := core.Chat{ID: 1, Title: "Team"}
	msg := func(id int, text string) core.Message {
		return core.Message{ID: id, ChatID: 1, From: "Ann", Text: text, Date: time.Unix(0, 0)}
	}
	be := scripted{
		backlog: []core.Message{msg(4, "four"), msg(5, "five")},
		live: []core.Event{
			{Kind: core.EventMessage, Chat: chat, Message: msg(5, "five")},     // also in the backlog
			{Kind: core.EventEdit, Chat: chat, Message: msg(2, "two, edited")}, // older than the cursor
			{Kind: core.EventMessage, Chat: core.Chat{ID: 9}, Message: msg(8, "elsewhere")},
			{Kind: core.EventMessage, Chat: chat, Message: msg(6, "six")},
			{Kind: core.EventMessage, Chat: chat, Message: msg(7, "seven")}, // past --count
		},
	}

	var stats watchStats
	var err error
	printed := captured(t, func() {
		stats, err = (&app{}).watch(context.Background(), be, watchOptions{chats: []string{"team"}, after: 3, count: 3})
	})
	if err != nil || stats.seen != 3 {
		t.Fatalf("watch saw %d messages: %v", stats.seen, err)
	}
	var texts []string
	for _, line := range strings.Split(strings.TrimSpace(printed), "\n")[1:] { // after the data marker
		texts = append(texts, line[strings.LastIndex(line, ": ")+2:])
	}
	if got := strings.Join(texts, ","); got != "four,five,six" {
		t.Errorf("printed %q, want four,five,six", got)
	}
}

// A file sent to a chat is named by its sender. Downloading it must not be
// a way to replace tg's own files, the guardrails among them.
func TestDownloadCannotTargetTheStateDirectory(t *testing.T) {
	if err := run(t, "config", "read-only", "on"); err != nil { // creates tg's directory
		t.Fatal(err)
	}
	for _, dir := range []string{config.Dir(), filepath.Join(config.Dir(), "accounts", "default")} {
		if err := run(t, "download", "alice", "5", "-o", dir); !refused(err) {
			t.Errorf("downloading into %s: %v", dir, err)
		}
	}
	// A link that leads into the directory is no way around, also when the
	// folder below it has yet to be created.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(config.Dir(), link); err != nil {
		t.Fatal(err)
	}
	if err := run(t, "download", "alice", "5", "-o", filepath.Join(link, "new")); !refused(err) {
		t.Errorf("downloading through a link: %v", err)
	}

	elsewhere := t.TempDir()
	if err := run(t, "download", "alice", "5", "-o", elsewhere); err != nil {
		t.Errorf("downloading elsewhere: %v", err)
	}
	if err := run(t, "download", "alice", "5", "-o", elsewhere); err == nil {
		t.Error("the second download replaced the first file")
	}
}

// A new session has no guardrails, and the login code is readable in the
// account's own chats: an agent must not be able to log in.
func TestLoginNeedsATerminal(t *testing.T) {
	t.Setenv("TG_CLI_HOME", t.TempDir())
	t.Setenv("TG_DEMO", "")
	t.Setenv("TG_API_ID", "1")
	t.Setenv("TG_API_HASH", "h")
	atTerminal(t, false)
	err := (&app{version: "dev"}).dispatch(context.Background(), []string{"login", "--account", "extra", "--phone", "+15550100"})
	if !refused(err) {
		t.Fatalf("logging in without a terminal: %v", err)
	}
}

// Adding an account must not change which one the others act as, also when
// the file was written by hand and names no default.
func TestLoginKeepsTheDefaultAccount(t *testing.T) {
	atTerminal(t, true)
	if err := run(t, "config"); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, `{"accounts": {"default": {"read_only": true, "send_to": []}}}`)
	if err := run(t, "login", "--account", "work"); err != nil {
		t.Fatal(err)
	}
	if err := run(t, "send", "alice", "hi"); !refused(err) {
		t.Errorf("the default moved away from the read-only account: %v", err)
	}
	if err := run(t, "config", "read-only", "on", "--account", "tpyo"); err == nil {
		t.Error("a guardrail was set on an account that does not exist")
	}
}
