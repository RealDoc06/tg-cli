// Package clipboard reads images from the system clipboard. Terminals only
// paste text, so an image copied elsewhere has to be fetched with the
// platform's own tools, or through the terminal with kitty's clipboard
// protocol (kitty.go). The tools read the clipboard of the machine tg runs on,
// which over SSH is not the one in front of the user; the terminal reads the
// user's own.
package clipboard

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// ErrNoImage is returned when the clipboard holds no image.
var ErrNoImage = errors.New("no image in the clipboard, copy one first")

// Image returns the image in the clipboard, encoded as PNG.
func Image(ctx context.Context) ([]byte, error) {
	var data []byte
	var err error
	switch runtime.GOOS {
	case "darwin":
		// AppleScript prints the data as «data PNGf89504E47...».
		var out []byte
		out, err = exec.CommandContext(ctx, "osascript", "-e", "the clipboard as «class PNGf»").Output()
		if err == nil {
			hexData := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(string(out)), "«data PNGf"), "»")
			data, err = hex.DecodeString(hexData)
		} else {
			err = ErrNoImage // osascript fails when there is nothing to convert
		}
	case "windows":
		const script = `Add-Type -AssemblyName System.Windows.Forms;` +
			`$i = [Windows.Forms.Clipboard]::GetImage(); if ($i) {` +
			`$m = New-Object IO.MemoryStream; $i.Save($m, [Drawing.Imaging.ImageFormat]::Png);` +
			`[Convert]::ToBase64String($m.ToArray()) }`
		var out []byte
		out, err = exec.CommandContext(ctx, "powershell", "-NoProfile", "-STA", "-Command", script).Output()
		if err == nil {
			data, err = base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
		}
	default:
		data, err = unixImage(ctx)
	}
	if err != nil {
		return nil, err
	}
	// Some clipboard owners, xclip among them, hand out text for any type
	// asked for, so the data has to prove it is a PNG.
	if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		return nil, ErrNoImage
	}
	return data, nil
}

// unixImage asks wl-paste on Wayland and xclip on X11.
func unixImage(ctx context.Context) ([]byte, error) {
	name, args := "xclip", []string{"-selection", "clipboard", "-target", "image/png", "-out"}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		name, args = "wl-paste", []string{"--no-newline", "--type", "image/png"}
	}
	if os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("DISPLAY") == "" {
		return nil, errors.New("no clipboard here: over SSH, images paste in kitty, or send them with `tg send --file`")
	}
	if _, err := exec.LookPath(name); err != nil {
		return nil, errors.New("reading images from the clipboard needs " + name + ", install it or paste the image's file path")
	}
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return nil, ErrNoImage // both fail when no image/png target is offered
	}
	return out, nil
}
