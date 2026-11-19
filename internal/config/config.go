// Package config owns tg's files: where they live, and config.json, the one
// file that holds the API credentials, the accounts and their guardrails.
// The `tg config` and `tg accounts` commands and a person with a text editor
// both change that same file, so neither can go stale.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
)

// DefaultAPIID and DefaultAPIHash can be baked into release builds with
// -ldflags "-X .../internal/config.DefaultAPIID=...", so users do not need to
// register their own Telegram application.
var (
	DefaultAPIID   string
	DefaultAPIHash string
)

// ErrNoCredentials means no api_id/api_hash is configured anywhere.
var ErrNoCredentials = errors.New("no Telegram API credentials, run `tg login`")

// DefaultAccount is the name of the account created by a plain `tg login`.
const DefaultAccount = "default"

// Dir is where tg keeps everything. TG_CLI_HOME overrides it.
func Dir() string {
	if home := os.Getenv("TG_CLI_HOME"); home != "" {
		return home
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "tg-cli")
}

// CacheDir holds data that can be deleted at any time, such as avatars.
func CacheDir() string {
	if home := os.Getenv("TG_CLI_HOME"); home != "" {
		return filepath.Join(home, "cache")
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(Dir(), "cache")
	}
	return filepath.Join(base, "tg-cli")
}

// Path returns the location of a file inside Dir.
func Path(name string) string { return filepath.Join(Dir(), name) }

// AccountDir is where one account keeps its session and peer cache.
func AccountDir(account string) string { return filepath.Join(Dir(), "accounts", account) }

// FilePath is the location of config.json.
func FilePath() string { return Path("config.json") }

// File is the content of config.json.
type File struct {
	APIID   int    `json:"api_id,omitempty"`
	APIHash string `json:"api_hash,omitempty"`
	// Account is the one commands use when none is named.
	Account  string              `json:"account,omitempty"`
	Accounts map[string]*Account `json:"accounts,omitempty"`
}

// Account is one logged in Telegram account and its guardrails. Both
// guardrail keys are always written so the file shows what can be set.
type Account struct {
	// User is who the account belongs to, for display only.
	User *User `json:"user,omitempty"`
	// ReadOnly forbids everything that changes the account: sending, marking
	// as read, creating groups.
	ReadOnly bool `json:"read_only"`
	// SendTo, when not empty, is the only chats the account may write to.
	SendTo []Chat `json:"send_to"`
}

// User describes the owner of an account.
type User struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username,omitempty"`
}

// Chat is an allowlist entry. Only ID counts, Title is there for the reader.
type Chat struct {
	ID    int64  `json:"id"`
	Title string `json:"title,omitempty"`
}

// Load reads config.json. A missing file is an empty configuration, a file
// that does not parse is an error: guessing would silently drop guardrails.
func Load() (*File, error) {
	f := &File{}
	data, err := os.ReadFile(FilePath())
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	// Unknown keys are refused: a misspelled guardrail must not pass for one
	// that is set.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(f); err != nil {
		return nil, fmt.Errorf("%s is not valid: %w", FilePath(), err)
	}
	return f, nil
}

// Save writes config.json, indented because people edit it.
func (f *File) Save() error {
	for _, account := range f.Accounts {
		if account.SendTo == nil {
			account.SendTo = []Chat{}
		}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(FilePath(), append(data, '\n'))
}

// Update applies change to the configuration as it is on disk right now.
func Update(change func(*File) error) error {
	f, err := Load()
	if err != nil {
		return err
	}
	if err := change(f); err != nil {
		return err
	}
	return f.Save()
}

// Active picks the account to act as: the one asked for on the command line,
// else TG_ACCOUNT, else the configured one, else the default.
func (f *File) Active(asked string) string {
	for _, name := range []string{asked, os.Getenv("TG_ACCOUNT"), f.Account} {
		if name != "" {
			return name
		}
	}
	return DefaultAccount
}

// Ensure returns the entry of an account, creating it if needed.
func (f *File) Ensure(name string) *Account {
	if f.Accounts == nil {
		f.Accounts = map[string]*Account{}
	}
	if f.Accounts[name] == nil {
		f.Accounts[name] = &Account{}
	}
	return f.Accounts[name]
}

// Names lists the accounts in a stable order.
func (f *File) Names() []string {
	names := make([]string, 0, len(f.Accounts))
	for name := range f.Accounts {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

var accountName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// CheckName rejects account names that would not make a safe directory name.
func CheckName(name string) error {
	if !accountName.MatchString(name) {
		return fmt.Errorf("invalid account name %q: use lowercase letters, digits, - and _", name)
	}
	return nil
}

// Migrate moves the session of versions that knew a single account into the
// default account. It does nothing once that is done.
func Migrate() error {
	legacy := Path("session.json")
	if _, err := os.Stat(legacy); err != nil {
		return nil
	}
	dir := AccountDir(DefaultAccount)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Rename(legacy, filepath.Join(dir, "session.json")); err != nil {
		return err
	}
	// The peer cache can be rebuilt, losing it is not worth failing over.
	_ = os.Rename(Path("peers.json"), filepath.Join(dir, "peers.json"))
	return Update(func(f *File) error {
		f.Ensure(DefaultAccount)
		if f.Account == "" {
			f.Account = DefaultAccount
		}
		return nil
	})
}

// Credentials identify the application to Telegram.
type Credentials struct {
	APIID   int
	APIHash string
}

// LoadCredentials looks at TG_API_ID/TG_API_HASH, then at config.json, then
// at the values baked into the binary.
func LoadCredentials() (Credentials, error) {
	fromStrings := func(id, hash string) (Credentials, bool) {
		n, err := strconv.Atoi(id)
		return Credentials{n, hash}, err == nil && n != 0 && hash != ""
	}
	if c, ok := fromStrings(os.Getenv("TG_API_ID"), os.Getenv("TG_API_HASH")); ok {
		return c, nil
	}
	f, err := Load()
	if err != nil {
		return Credentials{}, err
	}
	if f.APIID != 0 && f.APIHash != "" {
		return Credentials{f.APIID, f.APIHash}, nil
	}
	if c, ok := fromStrings(DefaultAPIID, DefaultAPIHash); ok {
		return c, nil
	}
	return Credentials{}, ErrNoCredentials
}

// Save stores the credentials for later runs.
func (c Credentials) Save() error {
	return Update(func(f *File) error {
		f.APIID, f.APIHash = c.APIID, c.APIHash
		return nil
	})
}

// ReadJSON decodes the file at path into v.
func ReadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// WriteJSON atomically writes v to path.
func WriteJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFile(path, data)
}

// writeFile replaces path atomically, readable by the owner only since these
// files sit next to the session keys.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
