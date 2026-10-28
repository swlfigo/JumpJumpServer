#!/bin/sh
# Regenerates internal/game/testdata/swift_vectors.json from the client's Swift
# game logic. Run on an Apple Silicon Mac so the floats match iPhone hardware.
# IOS_DIR defaults to the sibling JumpJump checkout (branch explore/multiplayer).
set -eu
cd "$(dirname "$0")/.."
IOS_DIR="${IOS_DIR:-../JumpJump}"
BIN="$(mktemp -d)/server-vectors"
swiftc -O "$IOS_DIR/JumpJump/GameLogic.swift" "$IOS_DIR/Tests/ServerVectors/main.swift" -o "$BIN"
"$BIN" > internal/game/testdata/swift_vectors.json
