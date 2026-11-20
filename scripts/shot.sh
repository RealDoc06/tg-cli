#!/usr/bin/env bash
# Takes a PNG screenshot of tg running in a real terminal (kitty, headless
# under Xvfb), so pull requests show what a change looks like, images included.
# An output ending in .gif records 3 seconds instead, for animations. One ending
# in .mp4 records the whole interaction, from just before the first key until
# the last one settles, for showing how something is used.
# Always runs against the demo data (TG_DEMO=1) so no real chat can leak.
#
# Usage: scripts/shot.sh <out.png> [--size WxH] [--font PT] [--key K]... [--text T]... [--action A]... -- <command> [';;' <command>]...
#
#   scripts/shot.sh docs/chats.png -- tg chats
#   scripts/shot.sh docs/tui.png --key down --key enter --text "hello" -- tg
#   scripts/shot.sh docs/two.png -- tg chats -u ';;' tg read alice -n 3
#   scripts/shot.sh docs/open.mp4 --size 860x1100 --font 24 --key down --key enter -- tg
#
# Commands separated by ';;' run one after the other, each under its own prompt.
# --key, --text and --action are replayed in order once the command is up.
# Keys use kitty's names (enter, down, tab, escape, page_up, ctrl+c ...).
# Actions are kitty's own, like paste_from_clipboard.
# Needs Linux with Xvfb and ffmpeg. kitty is downloaded on first use.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
out=${1:?usage: shot.sh <out.png> [--size WxH] [--key K] [--text T] -- <command>}
shift
size=1180x660
font=12
steps=()
while [ $# -gt 0 ]; do
  case $1 in
    --size) size=$2; shift 2 ;;
    --font) font=$2; shift 2 ;;
    --key) steps+=("key:$2"); shift 2 ;;
    --text) steps+=("text:$2"); shift 2 ;;
    --action) steps+=("action:$2"); shift 2 ;;
    --) shift; break ;;
    *) echo "shot.sh: unknown option $1" >&2; exit 2 ;;
  esac
done
[ $# -gt 0 ] || { echo "shot.sh: no command given" >&2; exit 2; }
commands=()
current=""
for word in "$@"; do
  if [ "$word" = ";;" ]; then
    commands+=("$current")
    current=""
  else
    current="${current:+$current }$word"
  fi
done
commands+=("$current")

kitty=${TG_KITTY:-$(command -v kitty || true)}
if [ -z "$kitty" ]; then
  dir=$HOME/.cache/tg-cli-dev/kitty
  kitty=$dir/bin/kitty
  if [ ! -x "$kitty" ]; then
    echo "shot.sh: downloading kitty into $dir" >&2
    tag=$(curl -fsSL -o /dev/null -w '%{url_effective}' https://github.com/kovidgoyal/kitty/releases/latest | sed 's|.*/||')
    mkdir -p "$dir"
    curl -fsSL "https://github.com/kovidgoyal/kitty/releases/download/$tag/kitty-${tag#v}-$(uname -m).txz" | tar -xJ -C "$dir"
  fi
fi

work=$(mktemp -d)
trap 'kill $(jobs -p) 2>/dev/null; rm -rf "$work"' EXIT

# The screenshot shows the working tree, not whatever tg is installed.
(cd "$root" && go build -o "$work/bin/tg" ./cmd/tg)

# Commands are read on fd 3 so their stdin stays the terminal.
printf '%s\n' "${commands[@]}" > "$work/commands"
cat > "$work/run.sh" <<EOF
export PATH="$work/bin:\$PATH" TG_DEMO=1 TG_CLI_HOME="$work/home" TG_NO_AUTOUPDATE=1
cd "$work"
while IFS= read -r line <&3; do
  printf '\033[38;5;245m\$\033[0m %s\n' "\$line"
  eval "\$line"
done 3< "$work/commands"
sleep 600
EOF

display=:$((RANDOM % 500 + 200))
Xvfb "$display" -screen 0 "${size}x24" >/dev/null 2>&1 &
sleep 1
export DISPLAY=$display LIBGL_ALWAYS_SOFTWARE=1
"$kitty" --config NONE \
  -o font_size="$font" -o background=#000000 -o foreground=#ffffff -o cursor_blink_interval=0 \
  -o remember_window_size=no -o initial_window_width="${size%x*}" -o initial_window_height="${size#*x}" \
  -o window_padding_width=6 -o enable_audio_bell=no -o allow_remote_control=yes \
  --listen-on "unix:$work/kitty.sock" bash "$work/run.sh" >/dev/null 2>&1 &

sleep 3
mkdir -p "$(dirname "$out")"
# A video records losslessly while the steps play, and is encoded afterwards
# so the encoder never drops frames.
if [[ $out == *.mp4 ]]; then
  ffmpeg -loglevel error -y -f x11grab -framerate 30 -video_size "$size" -i "$display" \
    -c:v libx264rgb -qp 0 -preset ultrafast "$work/raw.mkv" &
  recorder=$!
  sleep 1.2
fi
for step in "${steps[@]}"; do
  case $step in
    key:*) "$kitty" @ --to "unix:$work/kitty.sock" send-key "${step#key:}" ;;
    text:*) "$kitty" @ --to "unix:$work/kitty.sock" send-text "${step#text:}" ;;
    action:*) "$kitty" @ --to "unix:$work/kitty.sock" action ${step#action:} ;;
  esac
  sleep 0.6
done
sleep 1.5

case $out in
  *.mp4) kill -INT "$recorder"; wait "$recorder" || true
    ffmpeg -loglevel error -y -i "$work/raw.mkv" -c:v libx264 -preset veryslow -tune animation -crf 24 \
      -pix_fmt yuv420p -movflags +faststart -an "$out" ;;
  *.gif) ffmpeg -loglevel error -y -f x11grab -framerate 15 -video_size "$size" -t 3 -i "$display" \
    -vf "split[a][b];[a]palettegen[p];[b][p]paletteuse" "$out" ;;
  *) ffmpeg -loglevel error -y -f x11grab -video_size "$size" -i "$display" -frames:v 1 "$out" ;;
esac
echo "$out"
