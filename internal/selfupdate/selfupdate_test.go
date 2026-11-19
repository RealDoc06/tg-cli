package selfupdate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// serveRelease fakes the GitHub release pages for one tag and binary.
func serveRelease(t *testing.T, tag string, binary []byte, sum string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
	})
	mux.HandleFunc("/releases/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  some_other_file\n%s  %s\n", sum, sum, AssetName())
	})
	mux.HandleFunc("/releases/download/"+tag+"/"+AssetName(), func(w http.ResponseWriter, _ *http.Request) {
		w.Write(binary)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	previous := BaseURL
	BaseURL = srv.URL
	t.Cleanup(func() { BaseURL = previous })
}

func installed(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "tg")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestInstallReplacesOlderBinary(t *testing.T) {
	binary := []byte("new binary")
	serveRelease(t, "v1.3.0", binary, fmt.Sprintf("%x", sha256.Sum256(binary)))
	exe := installed(t)

	version, changed, err := install(context.Background(), "v1.2.9", exe)
	if err != nil || !changed || version != "v1.3.0" {
		t.Fatalf("install = %q, %v, %v", version, changed, err)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != string(binary) {
		t.Fatalf("binary holds %q", got)
	}
	if info, _ := os.Stat(exe); info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("binary is not executable: %v", info.Mode())
	}
}

func TestInstallKeepsCurrentVersion(t *testing.T) {
	serveRelease(t, "v1.3.0", []byte("new binary"), "unused")
	exe := installed(t)

	// A newer local build must not be downgraded either.
	for _, current := range []string{"v1.3.0", "v1.10.0"} {
		if _, changed, err := install(context.Background(), current, exe); err != nil || changed {
			t.Fatalf("install from %s = changed %v, %v", current, changed, err)
		}
	}
}

func TestInstallRejectsBadChecksum(t *testing.T) {
	serveRelease(t, "v1.3.0", []byte("tampered"), fmt.Sprintf("%x", sha256.Sum256([]byte("new binary"))))
	exe := installed(t)

	if _, changed, err := install(context.Background(), "v1.2.9", exe); err == nil || changed {
		t.Fatalf("install = changed %v, %v, want a checksum error", changed, err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatalf("binary was replaced with %q", got)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".tg-update-*")); len(left) != 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
}
