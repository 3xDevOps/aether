# Spike: Codex CLI with short-lived access tokens only

Version: `@openai/codex@0.166.0` (`codex-cli 0.166.0`, installed under /tmp, linux-x64 musl static binary). Source: tag `rust-v0.166.0`, `codex-rs/login/src/auth/{manager,storage}.rs`, `token_data.rs`, `oauth/client.rs`, `app-server/src/external_auth.rs`.

## Not run: no runtime evidence

Nothing below was observed. Every answer is **from code**.

The safety rule requires either a Docker network with only the mock, `--network none`, or an egress-proof MITM. This container offers none of them:

- No `docker`, no `tmux`, not root, no sudo.
- `unshare -n` and `unshare -rn` fail (`write failed /proc/self/uid_map: Operation not permitted`).
- Real internet is reachable (`curl https://api.openai.com/` returned HTTP 421).
- The codex binary is statically linked, so `LD_PRELOAD` cannot block `connect()`.
- `spike/netjail.c` tried a seccomp user-notif jail that allows loopback only. The kernel refused it: `seccomp: Device or resource busy`. The sandbox already holds the one user-notif listener.

I could not guarantee the CLI cannot reach the vendor, so I did not execute it. To get runtime evidence: run on a host with Docker (`--network none` plus a loopback mock) or with `unshare -rn`.

## Answers (all from code)

1. **Access token only.**
   - Loading: `FileAuthStorage::load` reads `auth.json` into `AuthDotJson`. `tokens.refresh_token` is a required `String` with no `#[serde(default)]`, so an empty string `""` parses, but a missing key is a deserialize error.
   - Required fields: `id_token` (a JWT string, parsed by `parse_chatgpt_jwt_claims`), `access_token`, `refresh_token` (may be `""`). `account_id` is optional.
   - Signatures: `decode_jwt_payload` only checks three non-empty dot-separated parts and base64url-decodes the payload. It never verifies the signature, so unsigned fake JWTs work. The `https://api.openai.com/auth` claim supplies `chatgpt_account_id`, plan type and user id.
   - Refusal: none for an empty refresh token. Managed-account-grant paths do require a non-empty refresh token (`manager.rs:2706`), but those are not used for ordinary requests.
   - Headers: not verified here. `ChatGPT-Account-Id` is `CHATGPT_ACCOUNT_ID_HEADER` in `http-client/src/openai_headers.rs`. Check the model-provider call path at runtime.

2. **Proactive refresh** (`AuthManager::auth()` → `proactive_refresh_reason`, `manager.rs:3333`):
   - If the access-token JWT has a parseable `exp`: refresh when `exp <= now + 5 min` (`CHATGPT_ACCESS_TOKEN_REFRESH_WINDOW_MINUTES`). `last_refresh` is then ignored.
   - Otherwise: refresh when `last_refresh < now - 8 days` (`TOKEN_REFRESH_INTERVAL`). A missing `last_refresh` means no proactive refresh.
   - Order inside a refresh (`refresh_token_with_reason`): take the in-process refresh lock, then **reload auth.json first** and skip the refresh if the file changed (`ReloadedChanged`). Only if unchanged does it POST to the refresh endpoint.
   - With an empty refresh token: it still POSTs `refresh_token=""`. A 400 `invalid_grant`, or any 401, becomes `Permanent`. That failure is cached for that credential snapshot, so later attempts fail without network. Other statuses are `Transient`.
   - In `auth()`, any error is logged and the stale cached auth is returned. The request goes out with the expired token and gets a 401.

3. **401 recovery.** `UnauthorizedRecovery` (`manager.rs:1949`):
   - Managed ChatGPT mode: step 1 `Reload` re-reads auth.json, but only if the on-disk `account_id` equals the one the process started with (else `Permanent` error "since logged out or signed in to another account"). Step 2 `RefreshToken` POSTs to the refresh endpoint. Then `Done` and the 401 surfaces.
   - So a server that rewrites auth.json with a fresh access token for the same account lets step 1 succeed, and the retry uses the new token with no restart.
   - Reads use `File::open` + `read_to_string` every time, so in-place rewrite and rename-replace are both seen. I did not test them; both are plain reads. An in-place truncate+write can be read half-written. That gives a serde error and a failed load. Rename-replace avoids that.
   - Not rewriting means step 2 runs and POSTs the refresh with an empty token, which fails.

4. **Refresh request** (`oauth/client.rs`, `request_chatgpt_token_refresh`):
   - `POST` to `https://auth.openai.com/oauth/token`, JSON body `{grant_type: "refresh_token", client_id: "app_EMoamEEZ73f0CkXaXp7hrann", refresh_token, refresh_reason: proactive_expiry|scheduled|unauthorized_recovery|forced}`.
   - Overrides: `CODEX_REFRESH_TOKEN_URL_OVERRIDE` sets the full URL and wins over everything. `CODEX_APP_SERVER_LOGIN_CLIENT_ID` overrides the client id. If the stored `tokens.oauth_provenance` is present, the issuer there picks the URL (`{issuer}/oauth/token`) and the client id. A server we control can therefore answer refreshes.
   - Response: `{id_token?, access_token?, refresh_token?}`, all optional. If `id_token` is present it must parse as a JWT. Present fields replace stored ones, and `last_refresh` is set to now.
   - Persistence (`persist_tokens`): takes `auth.lock` in `CODEX_HOME` (flock) plus an in-process mutex. It re-reads the file and checks it is still the same login (else "Account credentials changed during refresh"). It then writes with `OpenOptions truncate+write+create`, mode 0600. That is **in place, same inode**, not rename. It then calls `reload()`.
   - So the rotated token is written back into the file.

5. **Writes outside refresh.** `codex login` / `logout` (`AuthManager::logout`, `logout_with_revoke`) delete `auth.json` (`delete_file_if_exists`) under the `auth.lock` flock. Logout with revoke POSTs to the revoke URL, overridable via `CODEX_REVOKE_TOKEN_URL_OVERRIDE`. I found no write on startup. Read-only auth.json: reads work. Only a refresh would fail on write. `auth.lock` is created in the `CODEX_HOME` directory, so that directory must be writable by the process or refresh/logout fail with an I/O error. Whether startup creates `auth.lock` was not checked.

6. **`codex app-server` external tokens.** `account/login/start` with `{type: "chatgptAuthTokens", accessToken, chatgptAccountId, chatgptPlanType?}`, marked experimental and "FOR OPENAI INTERNAL USE ONLY".
   - Codex builds `ChatgptAuthTokens` auth in memory (`from_external_access_token`): the access token is parsed as a JWT and refresh_token is `""`. It is stored in the process-local ephemeral store, which `load_auth` checks first, so it overrides any auth.json.
   - On a 401, `UnauthorizedRecovery` runs in `External` mode: a single `ExternalRefresh` step. The app-server sends the client a server request `chatgptAuthTokens/refresh` with `{reason: "unauthorized", previousAccountId}`. The client answers `{accessToken, chatgptAccountId, chatgptPlanType}`, with a timeout. Codex re-validates against forced-login/workspace restrictions.
   - Proactive refresh is skipped when external auth is active (`auth()` just reloads). Refresh is 401-driven only.
   - Reach: only through the app-server JSON-RPC, so a host program must drive it. I did not check whether the TUI or `codex exec` start an in-process app-server; the plain CLI has no flag or env var for it. Related: `CODEX_ACCESS_TOKEN` env (`read_codex_access_token_from_env`) takes a personal access token or agent-identity JWT, not a ChatGPT access token.
   - For `codex-acp`: not examined. It most likely drives an app-server-style client.

## Verdict (from code, unverified at runtime)

(a) Yes, plausibly. A session `auth.json` can hold `refresh_token: ""`, an `id_token` and `access_token` that are unsigned or short-lived JWTs, and a good `account_id`. Codex parses it and never checks signatures. Codex re-reads the file on 401 and, when the cached token is within 5 minutes of `exp`, reloads before it tries to refresh. The server can keep it working by rewriting the file (prefer atomic rename; in-place truncate has a torn-read window) with a new access token, ideally before `exp - 5 min`, with the same `account_id`. The token also needs an `exp` claim, or else the 8-day `last_refresh` rule applies. Failure mode if the server is late: one failed refresh POST with an empty token to `auth.openai.com`, a cached permanent failure for that snapshot, and an expired-token 401.
(b) Yes. `CODEX_REFRESH_TOKEN_URL_OVERRIDE` (set in the session env) points the refresh at a Aether-run endpoint that accepts `{grant_type, client_id, refresh_token, refresh_reason}` and returns `{access_token, id_token?, refresh_token?}`. The session's `refresh_token` can then be an opaque Aether handle, and codex persists whatever the response returns. This is cleaner than file rewriting, but it needs the env var in every codex process, and codex will write the response into auth.json. The override must be the only refresh path: stored `oauth_provenance` is ignored when the override is set.

Both need runtime confirmation before relying on them, in particular the exact headers, the half-written-file behaviour, and whether the TUI honours the override.
