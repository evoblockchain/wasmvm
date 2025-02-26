#!/bin/bash
set -o errexit -o nounset -o pipefail

# See https://github.com/evoblockchain/wasmvm/issues/222#issuecomment-880616953 for two approaches to
# enable stripping through cargo (if that is desired).

echo "Starting x86_64-unknown-linux-gnu build"
export CC=clang
export CXX=clang++
cargo build --release --target x86_64-unknown-linux-gnu
cp target/x86_64-unknown-linux-gnu/release/libwasmvm.so artifacts/libwasmvm.x86_64.so

echo "Starting aarch64-unknown-linux-gnu build"
export qemu_aarch64="qemu-aarch64 -L /usr/aarch64-linux-gnu"
export CC_aarch64_unknown_linux_gnu=clang
export AR_aarch64_unknown_linux_gnu=llvm-ar
export CFLAGS_aarch64_unknown_linux_gnu="--sysroot=/usr/aarch64-linux-gnu"
export CARGO_TARGET_AARCH64_UNKNOWN_LINUX_GNU_LINKER=aarch64-linux-gnu-gcc
export CARGO_TARGET_AARCH64_UNKNOWN_LINUX_GNU_RUNNER="$qemu_aarch64"
cargo build --release --target aarch64-unknown-linux-gnu
cp target/aarch64-unknown-linux-gnu/release/libwasmvm.so artifacts/libwasmvm.aarch64.so
