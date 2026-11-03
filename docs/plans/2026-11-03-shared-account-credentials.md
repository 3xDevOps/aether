# Shared-Account Credentials: Spike Findings and Proposal

**Goal:** A session on a shared account keeps authenticating as the sharer,
while the recipient can no longer take, log out or overwrite the sharer's
login, and a revoke stops a running session's use.

**Status:** approved by the owner on 2026-11-03: Phase 0 and Phase 1, and
a canary before any existing share switches.

## Today

A run on another member's account bind-mounts the owner's
`~/.claude/.credentials.json` or `~/.codex/auth.json` read-write over the
launcher's own (`internal/scheduler/environment.go` `loginMounts`). The
runtime checks the inode before the agent starts (#649). The recipient's agent
holds the owner's refresh token, so:

- Root in the session can copy the refresh token, a login that outlives the
  share.
- `/logout` or `claude auth logout` POSTs the owner's refresh token to
  `platform.claude.com/v1/oauth/token/revoke`, ending the owner's login.
  Codex logout revokes the same way.
- `/login` writes the recipient's login into the owner's file.
- A revoke blocks new launches only. A running container keeps the mount.

## Spike

Claude Code 2.1.317 and Codex CLI 0.166.0 ran in Docker on an `--internal`
network. Every vendor host resolved to a local TLS mock and every token was
fake. No real login was read or used.

### Claude Code

| Behavior | Observed |
| --- | --- |
| Login file with `accessToken`, `expiresAt`, `scopes`, `subscriptionType` and no `refreshToken` | Runs. Sends `Authorization: Bearer` with `anthropic-beta: oauth-2025-04-20`, so usage counts as subscription OAuth. Never writes the file. |
| `expiresAt` in the past, no `refreshToken` | Still sends the token. No refresh attempt, no client-side refusal. |
| File rewritten during a session | Picked up on the next request, both in place (same inode) and by rename. The CLI checks the file's mtime before each request. |
| 401 | Re-reads the file and retries with a newer token. With no newer token and no refresh token it shows `Not logged in · Please run /login`. |
| Refresh | About 5 minutes before `expiresAt`, also when idle. Takes the directory lock `~/.claude/.oauth_refresh.lock` (stale after 60 s), re-reads the file, and skips the refresh when the file already holds a different token. Writes the result by rename. |
| Refresh URL | `https://platform.claude.com/v1/oauth/token`. `CLAUDE_CODE_CUSTOM_OAUTH_URL` accepts only an allowlist, so a server cannot answer refreshes. |
| `/logout`, `claude auth logout` | POSTs the refresh token to the revoke endpoint, then deletes the file. With no refresh token there is nothing to revoke. |
| Read-only login file, refresh due | Refreshes, fails to save silently, and loses the rotated refresh token. |

### Codex

| Behavior | Observed |
| --- | --- |
| `auth.json` with `"refresh_token": ""` | Runs. Sends `Authorization: Bearer` and `ChatGPT-Account-Id`. No refresh while the access token's `exp` is more than 5 minutes away. A file without the `refresh_token` key loads as signed out, with no error. |
| File rewritten during a session | Picked up on a 401 or at the 5-minute expiry check. Codex re-reads the file and retries, with the file replaced in place or by rename. |
| Refresh with an empty refresh token | POSTs it, gets an error, and fails the turn: `Failed to refresh token: 400 Bad Request`. |
| `CODEX_REFRESH_TOKEN_URL_OVERRIDE` | Sends refreshes to a server we control, which can return a new access token and an opaque refresh handle. Codex saves the handle in place. |
| Refresh lock | `flock` on `$CODEX_HOME/auth.lock`. Re-reads the file before refreshing and before saving. |
| Read-only `auth.json`, token expired | Repeats the refresh POST about 25 times in 2 s. Each one would spend a real single-use refresh token. |

### Verdict

Both CLIs run on a login holding only a short-lived access token. Both pick up
a token the server rewrites into the file, including after expiry. Neither
needs a refresh token in the session. **The client side is feasible.**

What the spike cannot prove without a real login is the server side: the real
token lifetimes, the vendor's response when the server refreshes with the
CLIs' OAuth client ids, and rotation under load. Never test these by replaying
a real refresh token.

## Proposal

### Session login copies

A recipient's run gets its own login file per account,
`<data-dir>/scheduler/logins/<run>/<account>/`, written by the server outside
every member home, so a write for an account a run has moved off never reaches
its new container. The file is bind-mounted read-only at the agent's login
path:

- Claude Code: `claudeAiOauth` with `accessToken`, `expiresAt`, `scopes` and
  `subscriptionType`. No `refreshToken`.
- Codex: `tokens` with `id_token`, `access_token`, `account_id` and
  `"refresh_token": ""`, plus `last_refresh`.

The server rewrites the file in place with one `write` after the truncate.
Both CLIs treat a torn read as a missing file and recover on the next read.
The server rereads a torn owner login too, and a listed account never falls
back to mounting the owner's file: a login that is not an OAuth sign-in gives
a signed-out copy. The owner's own sessions and environment keep their real
login file as today.

### Token pump

While a recipient run is live, the server reads the owner's login read-only,
the way `internal/quota` does. When the owner's access token changes, the
server rewrites every copy.

### Central refresh

The owner's own CLIs refresh only while one of them runs. When the owner's
access token is within 30 minutes of expiry and a recipient run needs it, the
server refreshes the owner's login itself:

- Claude Code: it takes `~/.claude/.oauth_refresh.lock` in the owner's home
  and re-reads the file. If the file is still due, it POSTs the refresh and
  writes the result in place, since a rename would detach the owner's pinned
  mounts.
- Codex: it does the same under `flock` on `~/.codex/auth.lock`.

Both CLIs re-read the file under the same lock before they refresh, so the
owner's running CLIs adopt the server's token rather than spend the rotated
one.

### Revoke

A revoke rewrites every copy for that grantee to a signed-out file and stops
pumping. The next request gets a 401 and the session shows the CLI's
`Not logged in` error. A copied access token stays valid until it expires,
which is hours for Claude Code. That bound replaces today's unbounded refresh
token.

### Identity check

The runtime check (#649) moves from the owner's inode to the copy's inode. The
copy is never in the launcher's home, so another of the launcher's containers
cannot detach it. That keeps #645, #649 and #668 fixed.

A usage move to another account rewrites the copy instead of restarting the
container.

### What a recipient can still do

- Use the access token for its remaining lifetime, from anywhere.
- Read the subscription type and plan, as today.

### The owner's own sessions

On 2026-11-03 about six Enhanced sessions on one account failed together with
`Authentication required` while new containers on the account worked. Claude
Code 2.1.287 (the ACP adapter's) and 2.1.317 rewrite the login in place when
the rename fails, and on `invalid_grant` blank the file they see. A blanked
shared file breaks new containers too, so the failed sessions most likely
held an older inode of the login whose refresh token another process had
spent. Session login copies remove this for recipients, whose sessions hold
no refresh token and whose copies the server rewrites. The owner's own
sessions keep the real login and stay exposed; giving them copies too is a
follow-up. A failed session recovers only in a new container: writing the
current login into its old inode forks the refresh-token chain.

## Decisions

1. Central refresh is approved: the server refreshes with the CLIs' client
   ids, and recipient sessions of `claude` and `codex` get short-lived access
   tokens.
2. Phase 1 ships behind `aether-server --session-login-accounts
   <member-id>,...`, which lists the accounts whose shares use session login
   copies. Every other share keeps today's mounts.
3. The canary lists only the owner's own account, shared with a test member
   the owner picks, through at least two real refreshes of each CLI. Phase 2
   waits for it to pass and for the owner's go.

## Phases

0. **Now:** document the trust boundary this spike measured (no behavior
   change). Then, after approval, make revoke stop a running session's use:
   a revoke moves the grantee's live runs off the account, or stops them when
   nothing else can run them.
1. Session login copies, pump and central refresh for `claude` and `codex`.
   Other agents keep today's mounts.
2. Retire `loginMounts` and the flag for `claude` and `codex` once the canary
   passes and the owner approves.
