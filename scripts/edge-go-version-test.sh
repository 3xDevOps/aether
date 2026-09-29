#!/bin/sh
# images/edge/Dockerfile builds with the Go of its golang base image: that
# image sets GOTOOLCHAIN=local, so go.mod's toolchain line has no effect
# there. Fails when the two versions differ.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(dirname -- "$script_dir")

# The same rule as the Makefile's GO_TOOLCHAIN: the toolchain line when it
# names a version, else the go directive.
want=$(awk '
	$1 == "toolchain" && $2 ~ /^go[0-9]/ { t = substr($2, 3) }
	$1 == "go" && $2 ~ /^[0-9]/ { g = $2 }
	END { print (t != "" ? t : g) }' "$repo/go.mod")
got=$(sed -n 's/^FROM .*golang:\([0-9][0-9.]*\)[-@].*/\1/p' "$repo/images/edge/Dockerfile")

if [ -z "$want" ]; then
	echo "edge-go-version-test: go.mod names no toolchain or go version" >&2
	exit 1
fi
if [ -z "$got" ]; then
	echo "edge-go-version-test: images/edge/Dockerfile has no FROM golang:<version> line" >&2
	exit 1
fi
if [ "$got" != "$want" ]; then
	echo "edge-go-version-test: images/edge/Dockerfile builds with golang:$got, but go.mod pins go$want; change the golang tag and its @sha256 digest in images/edge/Dockerfile to $want (or go.mod's toolchain line to go$got)" >&2
	exit 1
fi
