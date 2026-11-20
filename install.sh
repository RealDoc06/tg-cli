#!/usr/bin/env bash
# Installs tg, the Telegram CLI and TUI: https://github.com/RealDoc06/tg-cli
#
#   curl -fsSL https://tgc.li/install.sh | bash
#
# Works on Linux, macOS and Windows (Git Bash, MSYS2 or WSL). The binary goes
# to ~/.local/bin, or to $TG_INSTALL_DIR. tg keeps itself up to date after.
set -euo pipefail

# Everything runs from main so a download cut short cannot run half a script.
main() {
  local repo=RealDoc06/tg-cli os arch ext="" dir tmp asset want got

  case $(uname -s) in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    MINGW* | MSYS* | CYGWIN*) os=windows ext=.exe ;;
    *) fail "unsupported system: $(uname -s)" ;;
  esac
  case $(uname -m) in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
  esac
  # A shell running under Rosetta reports x86_64 on Apple silicon.
  if [ "$os" = darwin ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
    arch=arm64
  fi

  dir=${TG_INSTALL_DIR:-$HOME/.local/bin}
  asset=tg_${os}_${arch}${ext}
  tmp=$(mktemp -d)
  trap "rm -rf '$tmp'" EXIT # expanded now: tmp is gone by the time the trap runs

  echo "Downloading $asset"
  curl -fsSL "https://github.com/$repo/releases/latest/download/$asset" -o "$tmp/$asset" ||
    fail "download failed, see https://github.com/$repo/releases"
  curl -fsSL "https://github.com/$repo/releases/latest/download/checksums.txt" -o "$tmp/checksums.txt" ||
    fail "could not download the checksums"

  want=$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/checksums.txt")
  if command -v sha256sum >/dev/null; then
    got=$(sha256sum "$tmp/$asset" | cut -d' ' -f1)
  else
    got=$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)
  fi
  [ -n "$want" ] && [ "$want" = "$got" ] || fail "checksum mismatch for $asset"

  mkdir -p "$dir"
  chmod +x "$tmp/$asset"
  mv -f "$tmp/$asset" "$dir/tg$ext"
  echo "Installed $("$dir/tg$ext" --version) to $dir/tg$ext"

  case ":$PATH:" in
    *":$dir:"*) ;;
    *) echo "Add $dir to your PATH to run it as \`tg\`:  export PATH=\"$dir:\$PATH\"" ;;
  esac
  echo "Next: run \`tg login\`, then \`tg\`."
}

fail() {
  echo "install.sh: $*" >&2
  exit 1
}

main "$@"
