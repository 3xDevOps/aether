# Spike: Claude Code with short-lived access tokens only

Tested: Claude Code **2.1.317** (`npm i @anthropic-ai/claude-code@latest`, native Bun binary, Linux x64), 2026-11-03.
All tokens are invented (`fake-access-N`, `fake-refresh-N`). Nothing real was read or used.

## Method and isolation

- No Docker, no root, no netns capability here. Isolation instead: `guard.py` runs the CLI under a seccomp+ptrace
  egress guard. `connect()` to anything but AF_UNIX and 127.0.0.0/8 returns EACCES; UDP/raw sockets and `io_uring_setup`
  are refused. Every `connect()` is logged. Across all runs `guard.log` has **0 denied connects**; only 127.0.0.1:8801/8802 were dialled.
- `mock.py` serves `ANTHROPIC_BASE_URL=http://127.0.0.1:8801` (messages API) and, as an HTTPS-intercepting proxy on :8802
  (`HTTPS_PROXY`, throwaway CA via `NODE_EXTRA_CA_CERTS`), `api.anthropic.com` and `platform.claude.com` (OAuth token, profile, hello, event logging).
  Behaviour (401s, token endpoint status/delay/body) is switched per request from `config.json`.
- Every CLI ran with `env -i`, `HOME=/tmp/spike-home`, `CLAUDE_CONFIG_DIR=/tmp/spike-home/.claude`. Interactive runs are driven through a pty (`drive.py`).
- Scripts: `env.sh start.sh q*.sh drive.py show.py rewrite_inplace.py rewrite_keep_mtime.py guard.py mock.py`. Outputs: `runs/*.out`.
- `from code` = read from the bundled JS (extracted from the binary), not observed.

## Endpoints the CLI uses (prod; from code unless noted)

| Purpose | URL | Override |
|---|---|---|
| Messages | `${ANTHROPIC_BASE_URL}/v1/messages?beta=true` (observed) | `ANTHROPIC_BASE_URL` |
| OAuth token / refresh | `https://platform.claude.com/v1/oauth/token` (observed via proxy) | none usable, see Q4 |
| Revoke (logout) | `https://platform.claude.com/v1/oauth/token/revoke` (observed) | same |
| Profile | `https://api.anthropic.com/api/oauth/profile` (Bearer only; observed) | NOT `ANTHROPIC_BASE_URL` |
| Usage | `https://api.anthropic.com/api/oauth/usage` (from code) | same |
| Roles / create_api_key | `api.anthropic.com/api/oauth/claude_cli/{roles,create_api_key}` | same |
| Preflight (interactive start) | `GET api.anthropic.com/api/hello`, `GET platform.claude.com/v1/oauth/hello`; non-200 blocks startup with "Unable to connect to Anthropic services" (observed) | |
| Also hit at start (observed) | `/api/claude_cli/bootstrap`, `/api/claude_code_grove`, `/api/claude_code_penguin_mode`, `/api/eval/sdk-*`, `/api/event_logging/v2/batch`, `/mcp-registry/v0/servers`, `/api/oauth/organizations/<uuid>/referral/eligibility` | 404s are tolerated |

OAuth client_id in refresh bodies: `9d1c250a-e61b-44d9-88ed-5944d1962f5e` (public constant in the CLI).
`CLAUDE_CODE_CUSTOM_OAUTH_URL` exists but is **allowlisted** (from code): only `https://beacon.claude-ai.staging.ant.dev`,
`https://claude.fedstart.com`, `https://claude-staging.fedstart.com`; anything else throws "not an approved endpoint".
Observed with `http://127.0.0.1:8801`: the CLI still ran and made no OAuth/profile calls (the throw is swallowed). `CLAUDE_CODE_OAUTH_CLIENT_ID` only swaps the client_id.
The profile/usage/token hosts ignore `ANTHROPIC_BASE_URL`, so only a proxy+CA (or DNS) can serve them.

## Q1 - file with accessToken, no refreshToken

`{"claudeAiOauth":{"accessToken":"fake-access-1","expiresAt":<+8h>,"scopes":["user:inference","user:profile"],"subscriptionType":"max"}}`

- **Yes**, `claude -p "hi"` and interactive `claude` start and work. No warning, no login prompt. TUI header shows "Opus 5.5 · Claude Max" (from `subscriptionType`).
- Messages request headers (interactive; `-p` differs only in user-agent suffix `sdk-cli` and the beta list):
  `authorization: Bearer fake-access-1`, `anthropic-version: 2023-06-01`, `x-app: cli`, `anthropic-dangerous-direct-browser-access: true`,
  `anthropic-beta: claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27`. No `x-api-key`.
- It also calls `GET /api/oauth/profile` (Bearer only) at start, and other `api.anthropic.com` endpoints with `anthropic-beta: oauth-2025-04-20`.
- The file was **not touched**: inode 37536323 and mtime identical before/after (`runs/q1*`; re-checked in q3a).

## Q2 - expiry with no refresh token; in-place rewrite

Evidence `runs/q2.out`, `runs/q2.mock.log`. Interactive session, file expiring at +150 s, same inode 37486652 throughout.

| t | event | token on the next messages request |
|---|---|---|
| +11 s | msg "one" | fake-access-1 |
| +40 s | external in-place rewrite (truncate+write, same inode) to fake-access-2 (+2 h) | |
| +51 s | msg "two" | **fake-access-2** (no restart) |
| +91 s | in-place rewrite to fake-access-3, 80 s life | |
| +101 s | msg "three" | **fake-access-3** |
| +172 s | fake-access-3 passes expiresAt | |
| +191 s | msg "four", expired, nothing rewritten | **fake-access-3 is sent anyway**, mock 200, no 401, no OAuth call |
| +220 s | in-place rewrite to fake-access-4 | |
| +252 s | msg "five" | **fake-access-4** |

- Expired token and no refreshToken: the CLI never refuses client-side, never calls the token endpoint, never prompts `/login`; it sends the token and lets the server decide (`-p` with expiresAt 10 min in the past: sent, 200).
- It picks up rewrites because it stats `~/.claude/.credentials.json` and, when mtime changes, drops its in-memory copy (from code: `lastCredentialsMtimeMs` watcher; credentials read with `readFileSync`). It is an mtime check: a rewrite that restores the old mtime is invisible until a 401 (Q3).
- The CLI did not write the file in this scenario (final mtime = my last rewrite).

## Q3 - 401 from the messages API

- File token, 401 always (`runs/q3a.out`, `-p`): two requests ~0.6 s apart with the same token, then `Failed to authenticate. API Error: 401 mock auth failure`, exit 1. No token-endpoint call, file untouched.
- Disk has a newer token but mtime unchanged (`runs/q3b.out`, `rewrite_keep_mtime.py`): request with cached fake-access-1 -> 401; **re-reads the file**, retries 0.6 s later with fake-access-2 -> 200. (from code: 401 recovery adopts the disk token if it differs and is not within 5 min of expiry; tag `oauth_401_recovered_from_disk`.)
- 401 for every token, interactive: TUI shows `API Error · Retrying in 1s · attempt 1/10`, then **"Please run /login · API Error: 401 mock auth failure"** after two requests.
- No refresh attempt when the file has no refreshToken. With a refreshToken, 401 recovery tries the refresh (from code, not exercised).

## Q4 - with a refreshToken

Refresh body (JSON, `runs/q4B.out`): `grant_type:"refresh_token"`, `refresh_token`, `client_id`, `scope:"user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload user:plugins"`. Header `Content-Type: application/json`. Response read: `access_token`, `refresh_token` (kept if absent), `expires_in`, `scope`, optional `account`/`organization`; CLI then calls `/api/oauth/profile` with the new token.

- **When:** lazily, on the first API call once `now + 300000 >= expiresAt` (5 min before expiry; from code `vR`). Observed: expiresAt = start+420 s, no refresh at +10 s (also none with 600 s left, `q4A`); idle through the window opening at +120 s with no request or timer; refresh fired at +201 s with the next message (`q4i.out`), same request then used the new token. No background timer. Expired tokens are refreshed the same way.
- **Re-read + lock:** yes. Before and after taking the lock it re-reads the file; if the accessToken differs from the one it held it skips the refresh and adopts the file (from code, observed in q4r). Lock = `proper-lockfile`-style directory `<configdir>/.oauth_refresh.lock` (+ `.oauth_refresh.lock.owner`), stale 60 s, mtime refreshed every 5 s, retried up to 5 times at 1-2 s. Observed (`q4B`): lock dir exists during the POST (3 s delayed mock).
  - Lock held by someone else, in-place rewrite by an outside process 4 s in: CLI sent the **new** token, **no** POST to the token endpoint (`q4r.out`).
  - Lock held, no rewrite: waited ~8 s, then proceeded with the old (still valid) token, no refresh (`q4r2.out`).
- **Write-back:** yes. Success rewrote the file with the rotated tokens (`accessToken`, `refreshToken: fake-refresh-rotated`, new `expiresAt`, scopes) via temp file + rename (inode 37489481 -> 37523703). The write is atomic by rename, so an inode change is the signal.
- **Writes without refresh:** with a refreshToken in the file the CLI rewrites it on **every start** even when nothing is due (inode and mtime change, adds `subscriptionTypeSource:"sign_in"`, `rateLimitTier`; `q4A`). Without a refreshToken it did not (Q1).
- **Failure behaviour (important):**
  - Token endpoint 400 (`invalid_grant`), token still valid (`q4C`) or expired (`q4D`): CLI exits `Failed to authenticate: OAuth session expired and could not be refreshed`, sends no messages request, and **rewrites the file to `{"accessToken":"","expiresAt":0,"refreshToken":""}`** (blanks both tokens).
  - Token endpoint 500 (`q4E`, `q4F`): four POSTs in ~0.4 s, then proceeds with the existing token (even expired), file keeps tokens (still rewritten with enrichment fields).
- **Override:** the refresh URL cannot be pointed at a server we control via env (allowlist above). Reaching our endpoint needs `HTTPS_PROXY` + a trusted CA (what this spike did) or DNS. Separate mechanism (from code): enterprise **gateway sign-in** stores `enterpriseGateway{url, idpRefreshToken, tokenEndpoint}` in the credentials file and refreshes against `tokenEndpoint` with `grant_type=refresh_token` form post; `CLAUDE_CODE_USE_GATEWAY=1 ANTHROPIC_BASE_URL=... ANTHROPIC_AUTH_TOKEN=<jwt>` runs without a file (observed: `Authorization: Bearer <jwt>` to the base URL, no `oauth-2025-04-20` beta, no profile call, `runs/q6g.out`). This is not subscription OAuth; the server would proxy inference itself.

## Q5 - writes/deletes outside refresh; read-only file

- `claude auth status`: no write. `claude auth logout` (`q5a`, `q5b`): **unlinks `.credentials.json`**. With a refreshToken it first POSTs `https://platform.claude.com/v1/oauth/token/revoke` (body from code: `token`, `token_type_hint`, `client_id`), which revokes the owner's refresh token server-side; without a refreshToken no revoke call. `/logout` in the TUI is the same code path (from code, not driven).
- Starting with a refreshToken in the file rewrites it (Q4). Starting without one does not (Q1, Q3a).
- Read-only: **no Docker or mount capability here, so a true `-v file:file:ro` bind mount was not run.** Approximation (`q5c.sh`): credentials directory via `CLAUDE_SECURESTORAGE_CONFIG_DIR` set `chmod 555`, file `444`. Results: runs fine and sends the token with no refreshToken (`q5c1`) and with a still-valid refreshToken (`q5c2`, write silently fails, no error shown). Refresh due (`q5c3`): lock creation fails, no POST, proceeds with the old token. Caveat: in the real Aether layout the lock dir lives in the writable `~/.claude`, so a due refresh would POST and the write-back would then fail (EBUSY on rename, EROFS in place; from code write errors become `{success:false}`), losing the rotated refresh token. Not verified.

## Q6 - `CLAUDE_CODE_OAUTH_TOKEN` and `apiKeyHelper`

- `CLAUDE_CODE_OAUTH_TOKEN=fake-env-1`, no file (`q6a`): works. `Authorization: Bearer fake-env-1`, `anthropic-beta: claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27,...` (subscription OAuth), `x-app: cli`, no `x-api-key`. No profile call, no file written, model defaults to Sonnet (no `user:profile` scope known).
- Never refreshed. On 401 (`q6b`): two identical requests then `Failed to authenticate. API Error: 401 mock auth failure`. From code: a user-supplied env token is deliberately not replaced by the stored login ("keeping the user-supplied CLAUDE_CODE_OAUTH_TOKEN ... Mint a fresh token with `claude setup-token`"); `CLAUDE_CODE_OAUTH_401_WAIT_MS` makes it poll the env/FD token for a rotation, default 0 (remote sessions only get a default). `CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR` and `/home/claude/.claude/remote/.oauth_token` are read for the same token (remote-runner path; not tested).
- `apiKeyHelper` (`settings.json` `{"apiKeyHelper":"/tmp/helper.sh"}`, helper prints the token), `CLAUDE_CODE_API_KEY_HELPER_TTL_MS=15000` (`q6c.out`): the OAuth-shaped string is sent as **both** `Authorization: Bearer X` **and** `x-api-key: X`, with **no** `oauth-2025-04-20` beta, `x-app: cli`. That is the API-key shape, not subscription OAuth: the vendor would treat the value as an API key, so an OAuth access token there is expected to be rejected or billed as API (not verified, the mock accepts anything).
  Invocations: at first use; again at the first use after TTL expiry (+22 s, +25 s later); **and again immediately after a 401** (1 s later) - when the helper returned a new token the retry succeeded. So a helper whose output the server rotates works across expiry, but only as an API key.

## Verdict

(a) **Yes, with a caveat, for the file approach.** A login file with only `accessToken`/`expiresAt`/`scopes`/`subscriptionType` and **no refreshToken** is accepted by `claude -p` and the TUI, sent as `Bearer` with `oauth-2025-04-20` (subscription OAuth), never rewritten by the CLI, and never refused client-side at expiry. The server can keep it alive by rewriting the file **in place** (same inode, truncate+write) at any time: the CLI notices via mtime before the next request, even after the token has expired, and a 401 triggers a disk re-read for the case where mtime was not bumped. Rewrite well before `expiresAt`. In-place is not atomic: a reader can see a truncated file, which the CLI treats as absent (from code); keep the file small and write it in one `write()` after the truncate. Residual risks: `claude auth logout` / `/logout` by the teammate unlinks the file (and with no refreshToken revokes nothing); if the mount is ever read-only the rewrite must come from the host side. Never give the teammate a refreshToken: the CLI refreshes on its own at T-5 min, rotates the token behind the server's back, and on `invalid_grant` blanks the file.

(b) **Not via env/config.** The refresh URL is not overridable to our host (`CLAUDE_CODE_CUSTOM_OAUTH_URL` is an Anthropic-only allowlist), and the profile/usage endpoints ignore `ANTHROPIC_BASE_URL`. Serving `platform.claude.com`/`api.anthropic.com` ourselves requires an HTTPS proxy and a CA trusted by the CLI (works, shown here via `HTTPS_PROXY`+`NODE_EXTRA_CA_CERTS`, but it makes the container's trust store ours) - and a refresh endpoint still must tolerate 5-min-early refresh, rotation write-back, and the destructive `invalid_grant` path. The supported alternatives are (a), or running a local gateway: `ANTHROPIC_BASE_URL` pointed at an Aether proxy with `ANTHROPIC_AUTH_TOKEN` (or the enterprise gateway sign-in whose `tokenEndpoint` is configurable), where the server injects the real subscription token upstream. `CLAUDE_CODE_OAUTH_TOKEN` and `apiKeyHelper` are not substitutes for (a): the env token cannot be renewed in a running process (401 is final unless restarted), and `apiKeyHelper` sends an API-key-shaped request.
