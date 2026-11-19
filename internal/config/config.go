// Package config locates tg's files and resolves the Telegram API credentials.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

// Dir is where the session and settings live. TG_CLI_HOME overrides it, which
// is how several accounts can be used side by side.
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

// Credentials identify the application to Telegram.
type Credentials struct {
	APIID   int    `json:"api_id"`
	APIHash string `json:"api_hash"`
}

// LoadCredentials looks at TG_API_ID/TG_API_HASH, then at the saved settings,
// then at the values baked into the binary.
func LoadCredentials() (Credentials, error) {
	fromStrings := func(id, hash string) (Credentials, bool) {
		n, err := strconv.Atoi(id)
		return Credentials{n, hash}, err == nil && n != 0 && hash != ""
	}
	if c, ok := fromStrings(os.Getenv("TG_API_ID"), os.Getenv("TG_API_HASH")); ok {
		return c, nil
	}
	var saved Credentials
	if err := ReadJSON(Path("config.json"), &saved); err == nil && saved.APIID != 0 && saved.APIHash != "" {
		return saved, nil
	}
	if c, ok := fromStrings(DefaultAPIID, DefaultAPIHash); ok {
		return c, nil
	}
	return Credentials{}, ErrNoCredentials
}

// Save stores the credentials for later runs.
func (c Credentials) Save() error { return WriteJSON(Path("config.json"), c) }

// ReadJSON decodes the file at path into v.
func ReadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// WriteJSON atomically writes v to path, readable by the owner only since
// these files sit next to the session key.
func WriteJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
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
