#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"
revision=34f39002c6e3974b54e6a6d400bd83777c5ea558
archive_sha=932fede7be27fbd0bfdffbe7b42c25fea8c249ed1b52d92b50c1e599ffd41aec
build_dir=${REX_BUILD_DIR:-"$root/.build"}
zig=${ZIG:-zig}
if [[ "$zig" != /* ]]; then zig=$(command -v "$zig"); fi
if [[ $("$zig" version) != 0.16.0 ]]; then
  echo 'This Ghostty revision requires Zig 0.16.0. Set ZIG to that executable.' >&2
  exit 1
fi

target=${1:-native}
if [[ "$target" == native ]]; then target=$(go env GOOS)-$(go env GOARCH); fi
case "$target" in
  linux-amd64) zig_target=x86_64-linux-gnu; goos=linux; goarch=amd64 ;;
  linux-arm64) zig_target=aarch64-linux-gnu; goos=linux; goarch=arm64 ;;
  darwin-arm64) zig_target=aarch64-macos; goos=darwin; goarch=arm64 ;;
  darwin-amd64) zig_target=x86_64-macos; goos=darwin; goarch=amd64 ;;
  *) echo "Unsupported target: $target" >&2; exit 1 ;;
esac
mkdir -p "$build_dir" "$root/bin"

if [[ -z ${GHOSTTY_PREFIX:-} ]]; then
  archive="$build_dir/ghostty-$revision.tar.gz"
  if [[ ! -f "$archive" ]]; then
    curl --fail --location --retry 3 "https://codeload.github.com/ghostty-org/ghostty/tar.gz/$revision" -o "$archive.part"
    mv "$archive.part" "$archive"
  fi
  if command -v sha256sum >/dev/null; then
    actual_sha=$(sha256sum "$archive"); actual_sha=${actual_sha%% *}
  else
    actual_sha=$(shasum -a 256 "$archive"); actual_sha=${actual_sha%% *}
  fi
  if [[ "$actual_sha" != "$archive_sha" ]]; then
    echo "Ghostty archive checksum mismatch: $archive" >&2
    exit 1
  fi
  source_dir="$build_dir/ghostty-$revision"
  if [[ ! -d "$source_dir" ]]; then tar -xzf "$archive" -C "$build_dir"; fi
  export GHOSTTY_PREFIX="$build_dir/ghostty-$target"
  (cd "$source_dir" && "$zig" build -Demit-lib-vt=true -Demit-xcframework=false \
    -Doptimize=ReleaseFast -Dtarget="$zig_target" --prefix "$GHOSTTY_PREFIX" \
    --cache-dir "$build_dir/zig-cache-$target" -j"${JOBS:-4}")
fi

# libghostty's Go binding uses pkg-config. Select the pinned static library
# explicitly instead of a potentially incompatible system installation.
export PKG_CONFIG="$build_dir/pkg-config-ghostty"
cat > "$PKG_CONFIG" <<'SH'
#!/bin/sh
set -eu
for arg in "$@"; do
  case "$arg" in
    --cflags) printf '%s\n' "-I\"${GHOSTTY_PREFIX:?}/include\""; exit 0 ;;
    --libs) printf '%s\n' "\"${GHOSTTY_PREFIX:?}/lib/libghostty-vt.a\""; exit 0 ;;
  esac
done
exit 1
SH
chmod +x "$PKG_CONFIG"

export CGO_ENABLED=1 GOOS="$goos" GOARCH="$goarch"
if [[ "$target" != "$(go env GOHOSTOS)-$(go env GOHOSTARCH)" ]]; then
  export ZIG="$zig" REX_ZIG_TARGET="$zig_target"
  export CC="$build_dir/zig-cc-$target"
  cat > "$CC" <<'SH'
#!/bin/sh
exec "$ZIG" cc -target "$REX_ZIG_TARGET" -Wno-error=unused-command-line-argument "$@"
SH
  chmod +x "$CC"
fi
go build -trimpath -o "$root/bin/tuxosaurus-$target" ./cmd/tuxosaurus
echo "$root/bin/tuxosaurus-$target"
