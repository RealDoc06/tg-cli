package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/RealDoc06/tg-cli/internal/core"
)

// run dispatches a command line against the demo data, with stdout silenced.
func run(t *testing.T, args ...string) error {
	t.Helper()
	t.Setenv("TG_DEMO", "1")
	t.Setenv("TG_NO_AUTOUPDATE", "1")
	t.Setenv("TG_CLI_HOME", t.TempDir())
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	stdout := os.Stdout
	os.Stdout = null
	defer func() { os.Stdout = stdout }()
	return (&app{version: "dev"}).dispatch(context.Background(), args)
}

// Group and channel IDs start with a dash and must not be taken for flags,
// wherever they sit on the line.
func TestNegativeChatIDsAreArguments(t *testing.T) {
	for _, args := range [][]string{
		{"read", "-1001000000201", "-n", "2"},
		{"read", "-n", "2", "-202"},
		{"send", "-202", "hello"},
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
