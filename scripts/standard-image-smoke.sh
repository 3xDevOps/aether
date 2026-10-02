#!/bin/sh
# Run the exact local image natively before its architecture is published.
set -eu
[ "$#" -eq 1 ] || { echo 'usage: standard-image-smoke.sh <image>' >&2; exit 2; }

docker run --rm "$1" sh -euxc '
	go version
	node --version
	npm --version
	python3 --version
	uv --version
	rustc --version
	cargo --version
	rg --version
	jq --version
	gh --version
	git --version
	repo=$(mktemp -d)
	trap '\''rm -rf "$repo"'\'' EXIT
	git init -q --initial-branch=main "$repo"
	cd "$repo"
	git config user.name smoke
	git config user.email smoke@example.invalid
	tree=$(git mktree </dev/null)
	old=$(printf "old\n" | git commit-tree "$tree")
	git update-ref refs/heads/main "$old"
	new=$(printf "new\n" | git commit-tree "$tree" -p "$old")
	printf "start\nupdate HEAD %s %s\nprepare\ncommit\n" "$new" "$old" |
		git update-ref --stdin
	test "$(git rev-parse HEAD)" = "$new"
	test "$(git symbolic-ref HEAD)" = refs/heads/main
	test -x "$(git --exec-path)/git-remote-https"
	printf "protocol=https\nhost=example.invalid\nusername=smoke\npassword=smoke\n\n" |
		git credential-store --file="$repo/credentials" store
	test -s "$repo/credentials"
	ssh-keygen -q -t ed25519 -N "" -f "$repo/key"
	printf "signed\n" | git -c gpg.format=ssh -c user.signingkey="$repo/key" commit-tree -S "$tree" -p "$new"
	gpg --version
'
