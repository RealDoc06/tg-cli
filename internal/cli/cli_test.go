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
	capture, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	stdout := os.Stdout
	os.Stdout = capture
	defer func() { os.Stdout = stdout }()

	runErr := (&app{version: "dev"}).dispatch(context.Background(), args)
	printed, err := os.ReadFile(capture.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(printed), runErr
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
	if err := run(t, "watch", "--after", "3"); !errors.As(err, new(errUsage)) {
		t.Errorf("--after without a chat: %v", err)
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
	atTerminal(t, false)
	if err := run(t, "config", "allow", "alice"); !refused(err) {
		t.Fatalf("allowing a chat without a terminal: %v", err)
	}
	atTerminal(t, true)
	if err := run(t, "config", "allow", "alice", "-202"); err != nil {
		t.Fatal(err)
	}
	atTerminal(t, false)

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
	if err := os.WriteFile(config.FilePath(), []byte(byHand), 0o600); err != nil {
		t.Fatal(err)
	}

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
	if err := os.WriteFile(config.FilePath(), []byte(`{"accounts": {"default": {"readonly": true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
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
	listed, err := output(t, "accounts", "--json")
	if err != nil || strings.Count(listed, "\n") != 2 || !strings.Contains(listed, `"account":"default","default":true`) {
		t.Fatalf("accounts after two logins: %q, %v", listed, err)
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

	if err := run(t, "accounts", "remove", "work"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.Load(); cfg.Account != "default" || cfg.Accounts["work"] != nil {
		t.Errorf("after removing the default account: %+v", cfg)
	}
}
