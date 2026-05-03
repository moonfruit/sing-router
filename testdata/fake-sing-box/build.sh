#!/usr/bin/env bash
set -e
DIR="$(cd "$(dirname "$0")" && pwd)"
go build -o "$DIR/fake-sing-box" "$DIR"
