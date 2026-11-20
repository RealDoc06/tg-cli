#!/usr/bin/env bash
# Uploads screenshots to the repository's `screenshots` branch and prints the
# Markdown that embeds them, ready to paste into a pull request description.
# Keeping them on their own branch keeps binary files out of main's history.
#
# Usage: scripts/shot-publish.sh <file.png|file.gif>...
set -euo pipefail

[ $# -gt 0 ] || { echo "usage: shot-publish.sh <file.png>..." >&2; exit 2; }
repo=$(gh repo view --json nameWithOwner --jq .nameWithOwner)
branch=screenshots

if ! gh api "repos/$repo/git/ref/heads/$branch" >/dev/null 2>&1; then
  default=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name)
  sha=$(gh api "repos/$repo/git/ref/heads/$default" --jq .object.sha)
  gh api "repos/$repo/git/refs" -f ref="refs/heads/$branch" -f sha="$sha" >/dev/null
fi

for file in "$@"; do
  name=$(date +%Y%m%d-%H%M%S)-$(basename "$file")
  base64 -w0 "$file" > "${TMPDIR:-/tmp}/shot-publish.b64"
  gh api -X PUT "repos/$repo/contents/shots/$name" \
    -f message="screenshot: $name" -f branch="$branch" \
    -F content=@"${TMPDIR:-/tmp}/shot-publish.b64" >/dev/null
  alt=$(basename "$file")
  echo "![${alt%.*}](https://raw.githubusercontent.com/$repo/$branch/shots/$name)"
done
rm -f "${TMPDIR:-/tmp}/shot-publish.b64"
