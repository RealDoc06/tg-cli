// Package selfupdate keeps the tg binary current from GitHub releases. A
// release carries one raw binary per platform, named tg_<os>_<arch>[.exe],
// plus a checksums.txt in sha256sum format.
package selfupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/RealDoc06/tg-cli/internal/config"
)

// BaseURL is the repository the releases come from. Tests point it elsewhere.
var BaseURL = "https://github.com/RealDoc06/tg-cli"

// checkEvery is how often a launch looks for a new release.
const checkEvery = 6 * time.Hour

var httpClient = &http.Client{
	Timeout: 5 * time.Minute,
	// The latest release is found from where /releases/latest redirects to,
	// which, unlike the API, is not rate limited.
	CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		if strings.Contains(req.URL.Path, "/releases/tag/") {
			return http.ErrUseLastResponse
		}
		return nil
	},
}

// AssetName is the release file for the running platform.
func AssetName() string {
	name := fmt.Sprintf("tg_%s_%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// Latest returns the tag of the newest release.
func Latest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BaseURL+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	res.Body.Close()
	tag := path.Base(res.Header.Get("Location"))
	if !semver.IsValid(tag) {
		return "", fmt.Errorf("no release found (status %s)", res.Status)
	}
	return tag, nil
}

// Update installs the newest release over the running binary if it is newer
// than current. It returns the version now installed and whether it changed.
func Update(ctx context.Context, current string) (string, bool, error) {
	exe, err := os.Executable()
	if err != nil {
		return current, false, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return current, false, err
	}
	return install(ctx, current, exe)
}

// install is Update for the binary at exe.
func install(ctx context.Context, current, exe string) (string, bool, error) {
	if !semver.IsValid(current) {
		return current, false, fmt.Errorf("%q is a development build, install a release to get updates", current)
	}
	latest, err := Latest(ctx)
	if err != nil {
		return current, false, err
	}
	if semver.Compare(latest, current) <= 0 {
		return current, false, nil
	}

	release := BaseURL + "/releases/download/" + latest + "/"
	want, err := checksum(ctx, release+"checksums.txt", AssetName())
	if err != nil {
		return current, false, err
	}

	// Download next to the binary so the final rename stays on one filesystem.
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".tg-update-*")
	if err != nil {
		return current, false, fmt.Errorf("cannot write to %s, reinstall with the install script: %w", filepath.Dir(exe), err)
	}
	defer os.Remove(tmp.Name())
	got, err := download(ctx, release+AssetName(), tmp)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return current, false, err
	}
	if got != want {
		return current, false, fmt.Errorf("checksum mismatch for %s: got %s, want %s", AssetName(), got, want)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return current, false, err
	}
	if err := replace(exe, tmp.Name()); err != nil {
		return current, false, err
	}
	return latest, true, nil
}

// replace swaps the binary at exe for the file at fresh. A running Windows
// executable cannot be overwritten, but it can be renamed out of the way.
func replace(exe, fresh string) error {
	if runtime.GOOS != "windows" {
		return os.Rename(fresh, exe)
	}
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(fresh, exe); err != nil {
		_ = os.Rename(old, exe)
		return err
	}
	return nil
}

// download streams url into w and returns the SHA-256 of what was written.
func download(ctx context.Context, url string, w io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", url, res.Status)
	}
	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, sum), res.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// checksum returns the expected SHA-256 of an asset from a checksums file.
func checksum(ctx context.Context, url, asset string) (string, error) {
	var list strings.Builder
	if _, err := download(ctx, url, &list); err != nil {
		return "", err
	}
	scan := bufio.NewScanner(strings.NewReader(list.String()))
	for scan.Scan() {
		if fields := strings.Fields(scan.Text()); len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum published for %s", asset)
}

// AutoCheck is called on every launch. At most once per checkEvery it starts
// a detached `tg update` so the next launch runs the new version, without
// slowing this one down. TG_NO_AUTOUPDATE=1 turns it off.
func AutoCheck(current string) {
	if !semver.IsValid(current) || os.Getenv("TG_NO_AUTOUPDATE") != "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = os.Remove(exe + ".old") // left behind by a Windows update

	statePath := config.Path("update.json")
	var state struct {
		CheckedAt time.Time `json:"checked_at"`
	}
	_ = config.ReadJSON(statePath, &state)
	if time.Since(state.CheckedAt) < checkEvery {
		return
	}
	state.CheckedAt = time.Now()
	if err := config.WriteJSON(statePath, state); err != nil {
		return
	}

	cmd := exec.Command(exe, "update")
	detach(cmd)
	if err := cmd.Start(); err == nil {
		_ = cmd.Process.Release()
	}
}
