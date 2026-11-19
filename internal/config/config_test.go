package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Updating from a version with a single account must keep the user logged
// in: the session moves into the default account, once.
func TestMigrateKeepsTheSession(t *testing.T) {
	t.Setenv("TG_CLI_HOME", t.TempDir())
	if err := (Credentials{APIID: 7, APIHash: "h"}).Save(); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"session.json": "key", "peers.json": "{}"} {
		if err := os.WriteFile(Path(name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for range 2 { // the second run finds nothing left to do
		if err := Migrate(); err != nil {
			t.Fatal(err)
		}
	}

	moved, err := os.ReadFile(filepath.Join(AccountDir(DefaultAccount), "session.json"))
	if err != nil || string(moved) != "key" {
		t.Fatalf("session after migrating: %q, %v", moved, err)
	}
	if _, err := os.Stat(Path("session.json")); !os.IsNotExist(err) {
		t.Error("the old session file is still there")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Account != DefaultAccount || cfg.Accounts[DefaultAccount] == nil || cfg.APIID != 7 {
		t.Errorf("configuration after migrating: %+v", cfg)
	}
}

func TestActiveAccountPrecedence(t *testing.T) {
	f := &File{}
	t.Setenv("TG_ACCOUNT", "")
	if got := f.Active(""); got != DefaultAccount {
		t.Errorf("with nothing set: %q", got)
	}
	f.Account = "configured"
	if got := f.Active(""); got != "configured" {
		t.Errorf("with a configured default: %q", got)
	}
	t.Setenv("TG_ACCOUNT", "env")
	if got := f.Active(""); got != "env" {
		t.Errorf("with TG_ACCOUNT: %q", got)
	}
	if got := f.Active("flag"); got != "flag" {
		t.Errorf("with --account: %q", got)
	}
}

// A failed migration must leave the session where the next run finds it.
func TestMigrateRetriesAfterAFailure(t *testing.T) {
	t.Setenv("TG_CLI_HOME", t.TempDir())
	if err := os.WriteFile(Path("session.json"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(FilePath(), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(); err == nil {
		t.Fatal("migrating with an unreadable configuration succeeded")
	}
	if _, err := os.Stat(Path("session.json")); err != nil {
		t.Fatalf("the session was moved anyway: %v", err)
	}

	if err := os.Remove(FilePath()); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(AccountDir(DefaultAccount), "session.json")); err != nil {
		t.Errorf("the retry did not move the session: %v", err)
	}
}

// Several tg processes change the configuration at once, for example an
// agent tightening a guardrail while another command records the user.
// None of the changes may be lost.
func TestUpdatesDoNotOverwriteEachOther(t *testing.T) {
	t.Setenv("TG_CLI_HOME", t.TempDir())
	const writers = 16
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			err := Update(func(f *File) error {
				f.Ensure(fmt.Sprintf("account%d", i)).ReadOnly = true
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Accounts) != writers {
		t.Errorf("%d of %d changes survived", len(cfg.Accounts), writers)
	}
	if _, err := os.Stat(FilePath() + ".lock"); !os.IsNotExist(err) {
		t.Error("the lock file was left behind")
	}
}

// Trying the demo must never touch the real accounts and guardrails.
func TestDemoHasItsOwnDirectory(t *testing.T) {
	t.Setenv("TG_CLI_HOME", "")
	t.Setenv("TG_DEMO", "")
	normal := Dir()
	t.Setenv("TG_DEMO", "1")
	if demo := Dir(); demo == normal {
		t.Errorf("the demo uses %s", demo)
	}
}
