#!/bin/sh
# Publishes the Homebrew cask goreleaser wrote (dist/homebrew/Casks/peaka.rb)
# to peakacom/homebrew-tap, after adding the step that lifts macOS quarantine
# from the binary (goreleaser still writes the legacy `postflight` block, which
# Homebrew 7 deprecates; the cask is published with `postflight_steps`).
#   scripts/tap.sh            # commit and push Casks/peaka.rb to the tap
#   scripts/tap.sh --print    # only print the cask that would be published
# Needs a gh login that can push to the tap; without one, push the printed
# cask from a fork and open a pull request.
set -eu

TAP="peakacom/homebrew-tap"
SRC="dist/homebrew/Casks/peaka.rb"
[ -f "$SRC" ] || { echo "no $SRC: run goreleaser first" >&2; exit 2; }

render() {
  awk '
    /^  postflight do$/ { skip = 1; next }
    skip && /^  end$/ { skip = 0; next }
    skip { next }
    { print }
    /^  zsh_completion / {
      print ""
      print "  postflight_steps do"
      print "    run \"/usr/bin/xattr\", args: [\"-dr\", \"com.apple.quarantine\", \"{{staged_path}}/peaka\"], must_succeed: false"
      print "  end"
    }
  ' "$SRC" | awk 'NR == 1 || !(prev == "" && $0 == "") { print } { prev = $0 }'
}

if [ "${1:-}" = "--print" ]; then
  render
  exit 0
fi

version=$(sed -n 's/^  version "\(.*\)"$/\1/p' "$SRC")
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
gh repo clone "$TAP" "$work/tap" -- -q
mkdir -p "$work/tap/Casks"
render > "$work/tap/Casks/peaka.rb"
cd "$work/tap"
git add Casks/peaka.rb
if git diff --cached --quiet; then
  echo "the tap already has peaka $version" >&2
  exit 0
fi
git -c user.name=peaka-release -c user.email=dev@peaka.com commit -q -m "peaka v$version"
git push -q origin HEAD
echo "published peaka $version to $TAP" >&2
