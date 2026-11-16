#!/bin/sh
# Fails unless every user named is an admin of $GITHUB_REPOSITORY. A
# workflow passes $GITHUB_ACTOR and $GITHUB_TRIGGERING_ACTOR: a re-run
# keeps the dispatcher as GITHUB_ACTOR, so whoever started this attempt
# must be an admin too. A failed lookup refuses.
set -eu

repo="${GITHUB_REPOSITORY:?require-admin: GITHUB_REPOSITORY must name the repository}"
[ "$#" -gt 0 ] || { echo "usage: require-admin.sh <user>..." >&2; exit 2; }
allowed=true
for user in "$@"; do
	permission=$(gh api "repos/$repo/collaborators/$user/permission" --jq .permission 2>/dev/null || true)
	if [ "$permission" != admin ]; then
		allowed=false
		echo "::error::$user is not an admin of $repo (permission: ${permission:-unknown}); only admins may run this"
	fi
done
[ "$allowed" = true ]
