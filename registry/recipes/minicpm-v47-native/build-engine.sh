#!/usr/bin/env bash
# Build the engine client this recipe runs: jkuepker/TensorFold@minicpmv47-native
# (the branch that carries the minicpmv4_7 family; the 1.1.1 release has none)
# built with Zig on NVIDIA's PyTorch container. Run by the Hub with bash -lc in
# this directory when the image is missing; safe to re-run (BuildKit caches).
set -euo pipefail
cd "$(dirname "$(readlink -f "$0")")"

TAG="tensorfold-minicpm:zig-0b1fd2b"
ZIG_VERSION="0.17.0"
ZIG_SHA256="9e8d11661d4ae3bd57702a3832781e23ad151dde5798e16a5ccd503f65234ff8"

if [ ! -f zig.tar.xz ]; then
  url="https://ziglang.org/download/${ZIG_VERSION}/zig-aarch64-linux-${ZIG_VERSION}.tar.xz"
  # ziglang.org throttles one connection to ~35 KB/s; aria2c's parallel ranges
  # are an order of magnitude faster on this box.
  if command -v aria2c >/dev/null; then
    aria2c -x8 -s8 -k1M -q --console-log-level=warn -o zig.tar.xz "$url"
  else
    curl -fsSL -o zig.tar.xz "$url"
  fi
fi
echo "${ZIG_SHA256}  zig.tar.xz" | sha256sum -c -

docker build -t "$TAG" -f Dockerfile .
echo "built $TAG"
