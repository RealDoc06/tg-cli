package config

import (
	"os"
	"path/filepath"
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
