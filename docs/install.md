# Installing Aether

Two binaries, no dependencies of their own. `aether-server` runs on Linux and
needs Docker and git on the host; `aether` is the client and runs anywhere.

For the fastest path from nothing to a finished run, follow
[quickstart.md](quickstart.md). This file is the reference: what the installer
does, how to run the server as a service, and what lives in the data directory.

## Building from source

Source builds require Go 1.26+, GNU make, Bun 1.3+, and Node.js 22+. Bun
installs the web dependencies and drives the scripts; Node.js runs the Next
build and development server.

```sh
make dashboard         # build the static dashboard export in web/dist
make build             # dashboard, then the Go server, CLI and edge into dist/
cd web && bun run dev  # development server
```

The production web build is a Next static export in `web/dist`, embedded into
the Go server and CLI through `web/embed.go`. The CLI serves it from
`aether gui`; the server serves it when `--web-port` is set, so upgrading the
server binary is what refreshes the dashboard a phone loads. Running an
installed server or CLI needs no Node.js and no Next server.

The remote browser has its own Docker image. For an untagged, dirty, or
git-describe source build, build it locally before opening a browser session:

```sh
make browser-image
make browser-smoke
```

Both commands default to `aether/browser:test`; `BROWSER_IMAGE=<reference>`
selects another image. The smoke command runs the image's native Chromium
sandbox and companion checks, not a host-installed browser. See
[testing.md](testing.md#headless-browser-and-remote-development-acceptance).

## The install script

```sh
curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.sh | sh
```

It detects your OS and CPU, downloads the release assets it needs, and
verifies each one against the release's `checksums.txt`. macOS only ever gets
`aether`, because the server is Linux-only.

It asks one question first: is this machine the server, or a client? The
answer picks which binaries are installed, where they go, and what runs
afterwards.

| Answer | What is installed, and where | What it runs next |
| --- | --- | --- |
| `server` | On Linux both `aether` and `aether-server`, into `/usr/local/bin`, root-owned, using `sudo` when it has to; on a machine with no `sudo` at all it falls back to `~/.local/bin`. | `sudo aether-server setup` - the interactive server install below: listen address, data directory, tailnet policy, the dashboard port on a tailnet host, then the systemd activation line. |
| `client` | `aether` alone, into `~/.local/bin`, created if it is missing. No `sudo`, and the files stay yours. | `aether gui build` - builds and installs the desktop app. Nothing has to be installed first: the CLI downloads its own Node.js when the machine has none. |
| `none` | The same as `server`, `sudo` fallback included. | Nothing further. The binaries are installed and the script stops. |

A client gets `~/.local/bin` because the dashboard's **Update now** button
replaces the CLI from the `aether gui` process, which runs as you, and a
directory you own never asks for a password. A binary in a directory this
account cannot write, such as `/usr/local/bin`, still updates from the button
on macOS, through one administrator dialog (Touch ID or password); on Linux
the update prompt shows the `sudo aether update` to run instead (see
[Upgrading](#upgrading)).
`--bin-dir` overrides the choice for every role.

A client gets the CLI alone for the same reason: `aether update` reads an
`aether-server` next to the CLI as proof the machine is a server, so one
sitting in `~/.local/bin` would make every update pull a server binary this
machine never runs and make the dashboard ask for a
`sudo systemctl restart aether-server` that no unit backs. `--client`,
`--server` and `AETHER_COMPONENTS` still choose the components themselves.

If the install directory is not on your `PATH`, the script prints the one line
that adds it for your shell - bash, zsh, or fish, and a plain `export` when it
cannot tell - and never edits a profile for you. The desktop build records
the installed CLI's path, so the app starts without that directory on the
desktop session's `PATH`; a terminal still needs the line. An older
`aether` in `/usr/local/bin` is named by the script, which prints
the `sudo rm -f` that removes it, because that copy comes first on most
`PATH`s and would shadow the new one.

Enter takes the default: `server` on a Linux machine that got the server
binary, `client` everywhere else. Answers are case-insensitive. Choosing the
components yourself also answers this question, so `--client`, `--server` and
`AETHER_COMPONENTS` skip it; the platform default does not, which is why a Mac
is still asked.

The script normally arrives through a pipe, which means stdin is the script
itself, so the question and the command it launches read your terminal
(`/dev/tty`) instead. Where there is no terminal - CI, a Dockerfile, a
provisioning script - nothing is asked and nothing extra runs, the same as
`--role none`. It never blocks waiting for an answer that cannot come.

The script links the quickstart and ends on `next:`, the commands to run for
the role you picked, in bold orange on a terminal (`NO_COLOR` turns the color
off). When `aether-server setup` writes the service, setup's own `next:` ends
the run instead, followed only by the quickstart link, so the command that
starts the service appears once. Cancelling setup or the desktop build stops
the installer instead, preserving the interrupted command's exit status.

This script covers Linux and macOS. Windows uses the
[PowerShell installer](#windows-install-script) below.

The script needs `curl`, and `sha256sum` or `shasum`. With OpenSSL 3 on
`PATH` it also checks the release signature.

Options, as flags or environment variables:

| Flag | Variable | Effect |
| --- | --- | --- |
| `--version <tag>` | `AETHER_VERSION` | Install exactly this release, or fail if it is not complete. |
| `--bin-dir <dir>` | `AETHER_BIN_DIR` | Install somewhere else, whichever role is chosen. |
| `--client` | `AETHER_COMPONENTS=client` | CLI only. |
| `--server` | `AETHER_COMPONENTS=server` | Server only. |
| `--role <role>` | `AETHER_ROLE` | Answer the role question up front: `server`, `client`, or `none` to skip it. |
| | `AETHER_REPO` | Pull from a fork. |
| | `AETHER_BASE_URL` | Pull release assets from an HTTPS mirror; `latest` and the API still come from GitHub. |

Passing flags through a pipe needs `sh -s --`:

```sh
curl -fsSL .../install.sh | sh -s -- --client --bin-dir ~/bin
curl -fsSL .../install.sh | sh -s -- --role server
```

### Which release it installs

A release is complete for this install when it publishes `checksums.txt`,
every binary this OS, CPU and component choice needs, and a `checksums.txt`
line for each, plus `aether-release.json` and `aether-release.json.sig`.
Releases before `v0.7.0-alpha.12` predate signing and are complete without
those two files; from that tag on, a release missing either one is still
uploading, failed, or was tampered with.

Without `--version`, the script follows `github.com/<repo>/releases/latest`.
GitHub can list a release there while its assets are still uploading, or
after its build failed. When that tag is not complete, the script asks
`api.github.com/repos/<repo>/releases`, ignores drafts and prereleases, and
installs the highest semver release that is older than the latest tag,
signed and complete, saying why:

```
aether install: v0.7.0-alpha.12 is not fully published yet (no checksums.txt); installing v0.7.0-alpha.11, the newest complete release, for linux/amd64
```

`--version <tag>` installs exactly that tag, prereleases included, or stops:

```
aether install: v0.7.0-alpha.12 has no published aether-linux-amd64; it may still be building or may have failed - not installing a different version
```

Every file comes from the one chosen tag. When the release has a manifest,
the script verifies `aether-release.json.sig` against the release keys built
into it (the same keys as `internal/selfupdate/keys.go`), requires the
manifest's `version` to be the tag, and checks each binary's SHA-256 and
size against both the manifest and `checksums.txt`. Without OpenSSL 3 -
macOS ships LibreSSL - it says the signature was not verified and checks
the rest. A release from before signing is checked against `checksums.txt`
only, and the script prints that.

A checksum or size mismatch, a bad signature, a manifest that lists a
binary `checksums.txt` omits, a cut-off download or any network error stops
the install; none of them makes the script try another release. Every binary is downloaded and verified before any installed one is
replaced, so those failures leave the previous install as it was. Each
binary is then renamed into place from a copy in the same directory,
`aether-server` before `aether`; if a rename fails, the script names which
binaries are new and which are still the previous version. Rerunning is
safe. Only HTTPS is used. An unreachable GitHub is reported with curl's
error, and an API rate limit with its status:

```
aether install: GitHub API rate limit exceeded (HTTP 403 from https://api.github.com/repos/3xDevOps/Aether/releases?per_page=100, x-ratelimit-remaining: 0, x-ratelimit-reset: 1792209652); pass --version <tag> to install a specific release
```

The API is only asked when the latest release is not complete.

## Windows install script

In Windows PowerShell 5.1 or PowerShell 7, download the installer to a file,
review it, then run it:

```powershell
Invoke-WebRequest -UseBasicParsing -Uri https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.ps1 -OutFile "$env:TEMP\aether-install.ps1"
& "$env:TEMP\aether-install.ps1"
```

Windows is a client platform, so there is no server/client question. By
default the script downloads the native x64 or ARM64 CLI, verifies its SHA-256
against the release's `checksums.txt`, installs it at
`%LOCALAPPDATA%\Programs\Aether\aether.exe`, and runs that binary's
`gui build`, which downloads the release's prebuilt desktop app
([Desktop app](#desktop-app)). The result is the desktop app in the Start Menu
as **Aether**. Neither Node nor Go needs installing first.

Desktop setup requires `v0.5.1-alpha.4` or newer: the supported path needs the
separate desktop directory, direct-Node build, native shortcut creation, and
recorded CLI binding. In particular, `v0.4.0-alpha.6` predates the direct-Node
and native-shortcut fixes; it is not a supported desktop-install example.
Older releases are refused before downloading or replacing the CLI.
`-Role none` permits a CLI-only installation, including an older release:

```powershell
& "$env:TEMP\aether-install.ps1" -Role none
& "$env:TEMP\aether-install.ps1" -Version v0.4.0-alpha.6 -Role none
& "$env:TEMP\aether-install.ps1" -Version v0.5.1-alpha.4 -BinDir "$env:LOCALAPPDATA\AetherTools"
```

| Parameter | Variable | Effect |
| --- | --- | --- |
| `-Version <tag>` | `AETHER_VERSION` | Install exactly this release, or fail if it is not complete; otherwise install the newest complete release. |
| `-BinDir <dir>` | `AETHER_BIN_DIR` | Choose the CLI directory, never the desktop app's managed directory. |
| `-Role client\|none` | `AETHER_ROLE` | `client` installs the CLI and desktop; `none` stops after the CLI. The default is `client`, including unattended runs. |
| | `AETHER_REPO` | Download from a GitHub fork. |
| | `AETHER_BASE_URL` | Download from a release-asset mirror. |

The release is chosen as the [install script](#which-release-it-installs)
chooses it, for `aether-windows-<arch>.exe`, with these differences. PowerShell
has no ed25519 verifier, so the script does not check
`aether-release.json.sig`. It says so, and checks the binary's SHA-256 and
size against `checksums.txt` and `aether-release.json`. It refuses an
`AETHER_BASE_URL` that is not `https`.

Downloads use `%SystemRoot%\System32\curl.exe` (Windows 10 1803 and later)
and fall back to `Invoke-WebRequest` when it is missing or gets no HTTP
answer, for example behind a proxy only Windows' own settings name. curl.exe
runs with `-q`, so a `.curlrc` or `_curlrc` cannot turn off its TLS checks or
set a proxy. A connection that takes over 15 seconds, or a download that stays
below 1 KB/s for 60 seconds, is abandoned. If the installed CLI already has
the release's SHA-256, it is neither downloaded nor replaced; a CLI the
installer cannot read is downloaded and replaced. The old version
in `old -> new` is read from the installed `aether.exe`'s file properties,
without running it. The output is a numbered list of steps:

```
aether install: windows/amd64, CLI and desktop app
[1/3] downloading the newest complete release
      latest release: v0.7.0-alpha.13
      https://github.com/3xDevOps/Aether/releases/download/v0.7.0-alpha.13/
      checksums.txt                         1.0 KB  0.3s
      aether-release.json                   2.0 KB  0.3s
      aether-release.json.sig                 89 B  0.3s
      aether-windows-amd64.exe             30.3 MB  0.5s
      verified aether-windows-amd64.exe SHA-256 and size against checksums.txt and aether-release.json; aether-release.json.sig is not checked (PowerShell has no ed25519 verifier)
[2/3] installing the CLI: v0.7.0-alpha.12 -> v0.7.0-alpha.13
      C:\Users\<you>\AppData\Local\Programs\Aether\aether.exe
[3/3] installing the desktop app (the build output follows)
downloading https://github.com/3xDevOps/Aether/releases/download/v0.7.0-alpha.13/aether-desktop-windows-amd64.zip (99 MB)
  42 of 99 MB (42%)
  verified against the signed aether-release.json
installing the app
installed C:\Users\<you>\AppData\Local\Programs\Aether Desktop
launcher C:\Users\<you>\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Aether.lnk
open it from the Start Menu as Aether
      installed C:\Users\<you>\AppData\Local\Programs\Aether Desktop\aether-desktop.exe
next:
  open Aether from the Start menu, or link from a new terminal:
  aether link <server-host>:2222
done: Aether v0.7.0-alpha.13 installed in 24.4s
```

Step 3 runs `aether gui build` with its output unchanged, so its download
progress, and on a source build its npm and electron-builder messages, appear
as they happen. An error is printed once on stderr. Run with `-File`, or with
`&` or `.` at a prompt, the script then exits 1 and sets `$LASTEXITCODE`.
Called from another script or through `Invoke-Expression`, it throws instead,
so a caller's `catch` sees the failure and an interactive session stays
open.

Only the current user's `PATH` is updated, without duplicating the install
directory or removing other entries. Expandable entries such as `%SystemRoot%`
keep their registry type. The current PowerShell process also gets the
directory, and the change is broadcast, so terminals opened from Explorer or
the Start menu afterwards find `aether`. Terminals that were already open
keep their old `PATH`. An older CLI,
alias, or function can still shadow `aether`: `Get-Command aether -All`
shows which command runs. The installer never deletes a shadowing copy.
The desktop built by the installer records the CLI it just installed, so an
older Start Menu `PATH` does not change which CLI the app starts.

The desktop executable is separate from the CLI:
`%LOCALAPPDATA%\Programs\Aether Desktop\aether-desktop.exe`. Its `Aether.lnk`
is registered in the current user's Windows **Programs known folder**
(`FOLDERID_Programs`), including Windows/organization folder redirection.
Do not infer this folder by appending a Start Menu path to `%APPDATA%`;
the configured location can differ. See [shortcut diagnosis](#windows-start-menu-shortcut-diagnosis)
to inspect the actual registration.

For a fresh desktop installation, run the default command above. To reinstall
or upgrade, close Aether and rerun it, retaining `-BinDir` if you selected a
custom CLI location. Downloads and checksum failures leave the installed
binary alone. If `aether.exe` or `aether-desktop.exe` is running from the
install directories, or Windows reports `aether.exe` as locked, the installer
lists each running process with its pid, prints the `Stop-Process -Id <pids>`
and rerun commands, and changes nothing. When the sync daemon's Scheduled
Task `aether-daemon` exists, it also prints `schtasks /End /TN aether-daemon`:
the task restarts a daemon killed by `Stop-Process` within a minute. The
daemon starts again at the next sign-in, or at once with
`schtasks /Run /TN aether-daemon`. Any other failure to replace the CLI
reports the Windows error alone. A desktop-step failure keeps the installed
CLI, returns failure, and prints the `gui build` command to rerun.
Configuration and SSH files are untouched.

The installer does not request administrator access, change execution policy,
disable Defender, add exclusions, or unblock quarantined files. If policy
blocks unsigned scripts, use an approved script-signing process or the
[manual installation](#manual-install), not a Defender bypass. Downloads and
checksums come from the selected repository or mirror; use only one you
trust. Checksum verification is not code signing and does not guarantee that
Defender will accept an unsigned binary; see [Defender and SmartScreen](#windows-defender-and-smartscreen).

## Upgrading

**On a server host**, run:

```sh
sudo aether-server update [--version <tag>]
```

It installs the latest release (or `--version <tag>`) with the same
preflight, `.prev` copies, database snapshot and automatic rollback as the
dashboard's **Update now**, stops and starts the
`aether-server` systemd unit around the swap, and waits until the new binary
has either committed or been rolled back:

```
aether-server v0.9.0 -> v0.9.1
https://github.com/3xDevOps/Aether/releases/tag/v0.9.1
aether-server  v0.9.0 -> v0.9.1  restarted (pid 1234 -> 5678)
aether         /usr/local/bin/aether  v0.9.1
```

A rollback prints the recorded reason and exits non-zero; read the new
binary's log with `sudo journalctl -u aether-server -n 50`. When systemd
refuses to start the new binary at all, the command stops the unit, puts the
previous release back (and the database snapshot, if a start had already
migrated it), starts it, and exits non-zero with the `systemctl start` error.
With no `aether-server` systemd unit on the host, or with a unit that runs as
a non-root `User=` (the unprivileged setup in the unit's comment), it refuses
before changing anything: use `sudo aether update` and restart the server
yourself. It takes the data directory and listen address from the unit's
`ExecStart` and the `--config` file it names, and refuses when that
`ExecStart` runs another `aether-server` binary, naming the one to use. It
reads the database's schema without opening or migrating it. When the host
already has the latest release on disk but the unit still runs an earlier
file, as after `aether update`, it just restarts the unit onto the installed
binary (a stopped unit is started) and waits up to two minutes for the server
to accept connections on its `--addr`. A server that exits or restarts again
in that time fails the command with the unit's state.

**On a client**, `aether update` replaces the running CLI with the latest release only if it
is newer (or installs `--version <tag>`), verifying it against the release's
signed manifest ([Verified releases](#verified-releases)). On Linux, an
`aether-server` beside the CLI is updated too, but not restarted; restart it
yourself (`sudo systemctl restart aether-server` under a unit). On a host
where the `aether-server` systemd unit runs as root, `sudo aether update`
instead runs `sudo aether-server update [--version <tag>]`, which swaps,
restarts and rolls back the server; `--no-app` and `--force` do not apply
there. The command never asks
for privileges: a binary in a directory you cannot write, `/usr/local/bin` on
a stock install, is refused before anything is downloaded. The refusal names
the probe file it could not create (the number varies) and ends with the
command to run:

```
aether: open /usr/local/bin/.aether-update-probe-1234567890: permission denied: /usr/local/bin is not writable by this user; re-run as `sudo aether update`
```

Update the server before its clients: a server only moves to a release
whose client binaries are all published, and a client newer than its server
gains nothing. Before it installs a newer release, `aether update` asks the
linked server for its version, and refuses when the server is behind and you
are an admin of a server that can update itself:

```
aether: update the server first: aether server update (the server runs v0.8.0 and this installs v0.9.0; --force skips this check)
```

A member who is not an admin is told to ask one, and the install goes ahead.
So does an install with no linked server, an unreachable one, or one already
on the release.

Without `--version`, a client behind its linked server installs the server's
release rather than the latest one, so it never moves ahead of the server:

```
installing v0.9.0 to match the server
```

`--force` skips asking the server: no refusal, and no matching.

`sudo` usually resets `HOME` to root's, so `sudo aether update` finds no
link, asks no server, and installs the latest release. To match a server
that is behind it, pass the server's version, which `aether server update
--status` prints as `server version`: `sudo aether update --version
v0.9.0`. Where the binary sits in a directory you own, run `aether update`
without `sudo`.

With nothing to install, it prints the path of the binary it checked,
because `sudo` and `PATH` order can each pick a different `aether` than the
one your shell runs:

```
already on the latest release v0.7.0-alpha.6
this binary is /usr/local/bin/aether
```

After either outcome it warns when another `aether` comes first in `PATH`,
with the command that updates that one:

```
warning: aether in PATH is /home/ada/.local/bin/aether, a different binary; update it with: /home/ada/.local/bin/aether update
```

After an update, `journalctl -u aether-server` and `systemctl status
aether-server` warn when the installed unit (`/etc/systemd/system/aether-server.service`)
differs from the one the release ships. The warning names the command that
applies it, which rewrites only the unit and keeps `/etc/aether/server.conf`:

```
sudo aether-server install --sysbox=false && sudo systemctl daemon-reload && sudo systemctl restart aether-server
```

Put local changes in a drop-in (`sudo systemctl edit aether-server`), not in
the unit file, or the warning repeats.

Re-running either installer upgrades the client without changing its data.
On Windows, close Aether first and rerun `install.ps1`; the default also
rebuilds the desktop app. `aether update` still refuses on Windows.
Replacing the release binary manually remains an option below.

The server's default standard image follows the server build, so a server
update brings that release's environment image with it - though not into a
member environment that is already open
([environments.md](environments.md#the-standard-image)).

### When the server refuses an older client

A server refuses every client older than its own release, because an
older client calls methods and expects fields that the server has since
changed. The refusal is error
`-32005` (upgrade required), and the CLI prints it and exits 1:

```
aether: rpc error -32005: this aether is v0.7.0-alpha.11 and the server runs v0.8.0; update this client first: run `aether update` (desktop: Update in the app; Android: install the latest aether-android.apk), then retry
```

Run `aether update` on that machine, or **Update now** in the dashboard's
blocking update screen, then retry; both install the server's release. These keep working while the server
refuses the client, because none of them needs the server: `aether update`,
the gateway's `update.*` verbs, `server.info`, `server.update_status`,
and git fetch and push over SSH. Everything else is refused, including
streams and port forwards; a refused forward fails as `administratively
prohibited` with the same message.

An Android app from before this check has no blocking update screen: once
the server updates, its calls fail with the `-32005` refusal and the app
offers no update. Tell those users to install the
latest `aether-android.apk` from the release page by hand
([Android app](#android-app)).

The client sends its version in its SSH banner, `SSH-2.0-Aether
<version>`. A client from before this change sends none and is treated as
older than any release. Builds whose version is not a release (`dev`, a
bare commit) are never refused, and they never cause a refusal. A server
built past a tag (`v0.8.0-3-gabc123`) asks for that tag (`v0.8.0`), which
is what `server.info` reports as `min_client_version`. A client newer than
the server is accepted. `aether link` saves the link before it reports the
refusal. It sets no git remote for `--repo`, because that needs the
server, so re-run `aether link --repo` after `aether update`. A dashboard
claim or join through an edge still moves the clone's `aether` remote onto
the edge before it reports the refusal, because that is local git config.

A sync daemon (`aether daemon run`) checks every 30 seconds whether its
executable on disk changed. Once `aether update` replaces it, the daemon
exits 1 and its systemd, launchd or Windows task service starts the new
binary. About 30 seconds is how long the daemon takes to notice and exit;
the new binary then starts within seconds under systemd and launchd, and
after one minute under the Windows task.
A refused daemon logs the refusal and waits; its service is not restarted
into the same refusal. Every 5 minutes it reconnects, so a server that
rolled back its update or was downgraded gets its sync back without a
restart.

Daemons from `v0.7.0-alpha.12` and older do not do this. After updating from
one of them, restart the service once: `systemctl --user restart
aether-daemon` (macOS: `launchctl kickstart -k gui/$(id -u)/com.aether.daemon`;
Windows: `schtasks /End /TN aether-daemon`, then `schtasks /Run /TN
aether-daemon`).

### Verified releases

Each release since signing began publishes `aether-release.json`, listing
every asset's SHA-256 and size, and `aether-release.json.sig`, a base64
ed25519 signature over the file's exact bytes. The release workflow uploads
them with every other asset to a draft release and publishes it only after
downloading every asset back and matching it against the manifest
([Releases](#releases)). An updater - `aether update`, the
dashboard's **Update now**, `aether server update` - installs a release only
when:

- the signature verifies against a public key built into the running binary
  (`internal/selfupdate/keys.go`),
- the manifest's `version` is the tag being installed, and
- each downloaded binary has the manifest's size and SHA-256.

Downloads stream to disk, never read past the manifest's size, never follow
a redirect off https, and retry network and server errors three times with
backoff. A failure shows the real error and leaves every binary as it was. A
release without a manifest, which is every release from before signing, is
refused by name:

```
aether: release v0.7.0 has no signed aether-release.json (GET https://github.com/3xDevOps/Aether/releases/download/v0.7.0/aether-release.json: 404 Not Found): it predates signed releases or is still being published, and no updater installs it
```

Install such a release with the install script or by hand
([Manual install](#manual-install)). `checksums.txt` is still published for
those and for older clients.

**Verify a release by hand.** With `openssl` 3 and `jq`, in a directory
holding the downloaded asset, `aether-release.json` and
`aether-release.json.sig` from the same release:

```sh
cat > aether-release.pub <<'EOF'
-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAJ+AIq6GUVbRtEQ1ORqKJe3uh9JhvZkEQJQtYpxKbxD8=
-----END PUBLIC KEY-----
EOF
base64 -d aether-release.json.sig > aether-release.sig.bin
openssl pkeyutl -verify -pubin -inkey aether-release.pub -rawin \
  -in aether-release.json -sigfile aether-release.sig.bin
jq -r '.assets["aether-linux-amd64"].sha256' aether-release.json
sha256sum aether-linux-amd64
```

`openssl` prints `Signature Verified Successfully`, and the two digests must
match. On macOS use `shasum -a 256` in place of `sha256sum`.

The manifest also signs the release's images. `images` maps
`aether-browser`, `aether-edge` and `aether-standard` to the index digest
each published, and `edge` carries what a host updating a container edge
checks: `image`, `image_digest`, `store_schema` and `store_compat_floor` of
the edge database, the `protocol` range, `min_upgrade_from` and
`manual_steps`. `aether-edge release-check` reads them
([Verifying an image](edge.md#verifying-an-image)).

### Settings an update does not rewrite

`aether update` replaces binaries only. The systemd unit and the Sysbox
services keep the settings they were installed with, so an install that
predates these two needs a one-time step for each.

`OOMScoreAdjust=-500` makes a host-wide OOM kill an agent process before
`aether-server`. Add it as a drop-in and restart the server; runs keep
running and are re-adopted:

```sh
sudo mkdir -p /etc/systemd/system/aether-server.service.d
printf '[Service]\nOOMScoreAdjust=-500\n' | sudo tee /etc/systemd/system/aether-server.service.d/oom.conf
sudo systemctl daemon-reload && sudo systemctl restart aether-server
systemctl show aether-server -p OOMScoreAdjust
```

The last command prints `OOMScoreAdjust=-500`.

On a Sysbox host, `sysbox-mgr --disable-inner-image-preload` stops Sysbox
copying each container's inner `/var/lib/docker` into its writable layer on
every stop. Re-run the installer to add it:

```sh
sudo aether-server sysbox install
```

With containers running, it writes the setting but does not restart Sysbox:

```
install-sysbox: containers are running, so sysbox-mgr keeps its old flags until: sudo systemctl restart sysbox
```

Restarting Sysbox breaks running containers, so run that command when no
one is working.

### Environment image migration

The member environment image change is one-way. Workspaces no longer carry an
image; existing custom-image workspaces use the standard image after upgrade.
The `--neutral-image` server flag and the `env-edits/` data directory are
gone, and the bootstrap image is no longer published. Saved member images
live only in the server's Docker daemon; `aether env reset` removes a saved
image and returns that member to the standard image.

**From the dashboard.** The **Update now** button installs what is newest
at the click, or the linked server's release when the server is ahead of
this CLI, as `aether update` does: the version in the prompt is re-read
first, so it names the release the install is about to write. It runs the same swap from the
`aether gui` process, which runs as you. A CLI in
a directory you own - `~/.local/bin`, a Homebrew prefix - is replaced without
a question on macOS and Linux (Windows has no self-update). A CLI in a
directory this account cannot write, such as `/usr/local/bin`, splits by
platform, and the prompt says which case you are in before you click:

- **macOS.** The prompt says *macOS will ask for an administrator password:
  /usr/local/bin/aether is in a directory this account cannot write to. The
  dialog is labelled osascript, the tool Aether asks through. Aether never
  sees your password.* The button shows the standard macOS administrator
  dialog, once. The dialog is titled `osascript` because the request goes
  through `/usr/bin/osascript`, the system tool that asks for administrator
  rights on behalf of an app that is built locally and unsigned. Beneath the
  title is Aether's own text, quoted in
  [local-gateway.md](local-gateway.md#localv1-verbs), which names the file
  and the release; the last line is the system's own, "Touch ID or enter
  your password to allow this." The password or the Touch ID match goes to
  macOS's authorization service; Aether never sees it. Root then runs one
  fixed copy-and-verify command made of system tools, never Aether's own
  code ([security.md](security.md#client-self-update-on-macos) has the
  command). Cancelling the dialog, or a wrong password macOS gives up on,
  changes nothing: the prompt says *Update cancelled, nothing was changed.*
  and the button comes back. The button is offered only where the dialog
  can install: the gateway must be in a GUI login session (not started over
  SSH), and only root can write the binary's directory or any directory
  above it. Anywhere else the prompt shows `sudo aether update` instead;
  the full rule, and why, sits beside the quoted text in local-gateway.md.
- **Linux.** No button. The prompt shows `sudo aether update` to run in a
  terminal instead.

**It rebuilds the desktop app too.** The dashboard ships inside the CLI, but
the Electron shell around it does not, so once the binaries are swapped
`aether update` looks for an installed app (the table under [Desktop
app](#desktop-app)) and runs `aether gui build` with the binary it just
installed - the new one, because the shell sources ship inside it. The build
output streams to your terminal. Skip it with `--no-app`:

```sh
aether update --no-app
```

The rebuilt app starts that CLI path rather than whichever older copy happens
to come first on the desktop session's `PATH`. `AETHER_BIN` still overrides it
when you deliberately choose another binary. If the recorded CLI is moved or
deleted, set `AETHER_BIN` or rebuild the app with the installed CLI instead of
silently falling back to a different copy.

A machine with no app installed builds nothing and downloads nothing. A
server host with a systemd unit skips this step: `aether update` prints the
rebuild command instead. If the app is running when the rebuild
finishes, the command says to restart it. A rebuild that fails prints the
build's own error and the command to rerun, and exits non-zero - but the CLI
update itself already succeeded, and the message says so.

Under `sudo` the rebuild drops back to the invoking account (`SUDO_USER`) with
`sudo -u <user> -H`, so the app, the build directory and the Node and Electron
caches all land in that account's home owned by that account. Without it root
would build an app the user cannot rebuild.

**The check.** `aether update --check` reports whether a newer release exists
and exits 0 either way; `--check --json` prints one JSON object for a script:

```json
{"version":"v1.2.3","commit":"abc1234","latest":"v1.3.0","update_available":true,
 "asset":"aether-linux-amd64","release_url":"https://github.com/3xDevOps/Aether/releases/tag/v1.3.0",
 "dev":false,"disabled":false,"can_self_update":true,"checked_at":"2026-09-02T10:00:00Z"}
```

It resolves the tag from the GitHub releases redirect, with no token and no
rate limit, and counts a release only once its signed manifest is published.
Until then it reports no update and prints `no signed release is published
yet (running <version>)`. An answer is reused for an hour. A failed check is
reused for five minutes, doubling with each failure in a row up to an hour,
give or take a tenth. A build whose version is `dev` never reports an update.
Set `AETHER_NO_UPDATE_CHECK` to any non-empty value to stop every release
check on an air-gapped machine: the CLI's, the `aether gui` startup line, and
the dashboard's update prompt all answer `disabled` without touching the
network.

A binary built from a checkout reports what `git describe` produced
(`v1.2.3-4-gabc123`, plus `-dirty` for uncommitted changes). The comparison
reads that as the tag it descends from *plus* commits on top, so such a build
is never told to downgrade to that tag, and `aether update` without
`--version` does not replace it with that older release. It is still told
about a genuinely newer release. A checkout with no tags in reach reports a
bare commit, which cannot be ordered against anything and never updates
automatically; `--version <tag>` explicitly selects a release instead.

**In the dashboard.** `aether gui` runs the same check in the background and
prints one line to stderr when a newer release exists. The dashboard's
sidebar shows a one-line notice naming the new version; its **Update** link,
or **Update…** in the sidebar footer menu or Settings > **Server**, opens the
**Updates** dialog. Each pending update is one dismissible prompt there, and
the CLI's has an **Update now** button that replaces the binary on this
machine. It re-checks about every half hour while
the window is on screen, and again whenever you come back to it, so a release
that lands after launch shows up without restarting the app. The restart takes
the gateway's own work with it - attached terminals and any running
`aether sync` session stop, while the runs themselves keep going on the
server. Dismissing silences that version only - the next release shows the
prompt again.

The button does the same two steps the command does. It swaps the binaries,
then rebuilds the app when one is installed, and the prompt follows along:
*Updating the CLI…*, then *Rebuilding the app (about a minute; the first
time also fetches Node)…*, then *Relaunching*. On macOS with a binary in
a directory this account cannot write, the first step reads *Downloading
v1.3.0, then macOS asks for an administrator password…* and the dialog
(Touch ID or password) opens once the download is verified; cancelling it
ends the update there with nothing changed.
**Update now** stays disabled until it is over. In the desktop app the shell relaunches itself onto the new
build, so the window you end up in is the new one. In a browser tab the
gateway never exits (it is your terminal's process, not the app's): the app is
still rebuilt, and the prompt tells you to restart it.

A rebuild that fails does not cost you the CLI update. The gateway records the
build's error, the desktop app comes back on the new CLI in the old shell, and
the "desktop app is out of date" prompt then shows that error above the
`aether gui build` to run by hand. A successful build clears it.

On a single-box install the same update replaces the `aether-server` beside
the CLI, and prints the restart the running server still needs:

```
aether-server on disk is now v0.9.0; the running server keeps its old version until you restart it: sudo systemctl restart aether-server
```

Administrators see a second prompt when the **server** is behind the latest
release. An admin updates it from their laptop, no shell on the server box
needed:

```sh
aether server update [--version <tag>] [--when now|idle] [--allow-downgrade] [--cancel] [--yes]
aether server update --status
```

**Preflight.** Before it downloads anything, the server fetches the release's
signed manifest, `aether-release.json`, and checks that clients can follow
it: `aether-{linux,darwin}-{amd64,arm64}`,
`aether-windows-{amd64,arm64}.exe` and `aether-android.apk` must each be
listed and answer an anonymous request with the size the manifest records.
A release that fails is refused, naming the asset and its URL:

```
aether: rpc error -32002: release preflight failed: aether-android.apk (https://github.com/3xDevOps/Aether/releases/download/v0.9.0/aether-android.apk): HEAD answered 404 Not Found
```

**Replaced binary.** If `aether-server` on disk is no longer the file the
running process started from, as after `sudo aether update` without a
restart, the server refuses to update and reports `capable: false` with the
reason `restart onto it: sudo systemctl restart aether-server`. Updating then
would keep the already-new file as `.prev`.

**Downgrades.** A tag older than the running release is refused unless
`--allow-downgrade` is passed, which only works with `--when now`; the tag
the server already runs is always refused. Without `--version` the server
takes the latest signed release only when it is newer than the running one,
and otherwise answers `no update: aether-server v0.9.0 is not older than the
latest release v0.9.0`. A release whose manifest records an older database
schema than this server's database is always refused: that binary could not
open the database. `aether update` on a server host refuses such a release
the same way, naming both schemas:

```
aether: refusing to install aether-server v0.9.0: it knows store schema 90 and this build's server migrates the database to schema 91
```

**The swap.** `--when now` (the default) downloads both binaries and
verifies them against the manifest before replacing either, then renames
`aether-server` and the `aether` beside it into place. The files they
replace stay beside them as `aether-server.prev` and `aether.prev`. Until
both renames are done, `<data-dir>/server-update/swap.json` says `swapping`,
and a start that finds it so puts the `.prev` files back. The server then
closes its listeners and the database. When the release migrates the
database, it copies it to `<data-dir>/server-update/aether.db.pre-update`
now, so the copy holds every write. A new binary that starts without that
copy - the host rebooted between the swap and the copy - takes it before it
opens the database, and rolls back if it cannot. Under the shipped systemd unit the
server then exits with status 75 (`EX_TEMPFAIL`) and systemd starts the new
binary: a new PID, and the stop and start show in `systemctl status` and
`journalctl -u aether-server`. The unit's `RestartForceExitStatus=75` and
`SuccessExitStatus=75` make systemd restart on that status without logging a
failure. A unit installed before this release has neither line, but
`Restart=on-failure` restarts the server on any non-zero exit, so updates
work the same and the journal logs the exit as `Failed with result
'exit-code'`. `sudo aether-server install` rewrites the unit and keeps
`/etc/aether/server.conf`; run `sudo systemctl daemon-reload` afterwards. A
foreground `aether-server serve`, with no systemd unit around it, re-executes
the new binary in place with the same argv, environment and PID.

**Rollback.** The new binary records the update `applied` only after it has
opened and migrated the database, bound its SSH listener, and recovered the
runs and terminals from before the restart, and then deletes the database
copy. A migration may take as long as it needs; once
it finishes, the binary has 5 minutes to bind and record, or it exits. If it
exits before recording more than twice, or the next start finds those 5
minutes gone, that start restores the database copy when the schema already
moved, puts the `.prev` binaries back, records `rolled_back` with the error,
and starts the previous binary the same way. A re-exec that fails outright is undone
the same way. A restored copy loses every write made after it, and the error
says so with the time of the copy. A rollback that cannot restore a file
records why and exits with that error instead of executing anything.
Until the new binary has recorded `applied`, another update is refused
with `a server update is already being applied: v0.9.0 is still verifying
its first start (...)`. `--status` shows the outcome until the next
update:

```
update        rolled_back v0.8.0 -> v0.9.0
error         v0.9.0 started 2 times without finishing its boot: store: apply migration 91: ...; database restored to its snapshot of 2026-10-16T09:12:03Z; changes made after it are lost
```

`aether server update` prints each phase - checking the release, applying,
restarting - then reconnects and exits 0 only once `server.info` reports the
new version. A rollback, a failure, or a server that is not back on the new
version within 8 minutes exits non-zero with the server's own error.

**Release checks.** The server checks for a new signed release itself every
6 hours, give or take 10%. A failed check is retried after 5 minutes,
doubling up to 6 hours; the schedule survives restarts. `--status` prints the
latest release, when it was checked, and a failed check's error verbatim. A
build without a release version, or a server with `AETHER_NO_UPDATE_CHECK`
set, does not check.

**Recovering by hand.** When the new binary cannot start far enough to roll
itself back, restore the previous one on the server host. The database
lines are only needed if `<data-dir>/server-update/aether.db.pre-update`
exists, which it does only while an update that migrates the database has
not been recorded `applied`:

```sh
sudo systemctl stop aether-server
sudo mv /usr/local/bin/aether-server.prev /usr/local/bin/aether-server
sudo mv /usr/local/bin/aether.prev /usr/local/bin/aether
sudo rm -f /var/lib/aether/aether.db-wal /var/lib/aether/aether.db-shm
sudo mv /var/lib/aether/server-update/aether.db.pre-update /var/lib/aether/aether.db
sudo systemctl start aether-server
```

The restored server records the update as rolled back by hand.

`--when idle` instead runs the preflight, then records one pending update,
applied the first time no
run is working, no terminal is attached and no SSH connection into a run
([terminal.md](terminal.md#ssh-and-editors)) is open. Two kinds of run do not hold
it back: one parked at `needs-attention`, waiting on a person, and one
paused with `aether pause`, whose container is frozen. Neither has anything
running inside it and both survive the restart like any other run. An SSH
connection left open into a paused run still holds the update back. A second
`--when idle` call replaces the pending one, and `--cancel` clears it. The
update stays pending until the swap, so a server that stops mid-download
retries it at the next idle moment; an `--allow-downgrade` update is not
left pending and has to be run again. A release it cannot fetch at that
moment - its manifest, a binary, or a client asset the preflight checks
answering with a network error, 5xx or 429 - is retried at the next idle
poll, up to three attempts. A run that starts while the release
downloads puts the previous binaries back, and the update waits for the
next idle moment.

A run that survives the restart keeps the Aether helper binary its
container was created with. What a newer server adds to that helper, such as
file transfer and port forwarding over ssh
([terminal.md](terminal.md#ssh-and-editors)), is there for runs launched
after the upgrade.

`--yes` skips the confirmation prompt. `--status` prints the running
version, the latest release and its check, whether this server can update
itself, the current or last update's phase and error, any pending update and
what it is still waiting for, and the outcome of the last attempt.
`server update` is admin only; any member can read `--status`.

Runs keep going through the restart: the scheduler reattaches to their live
containers when the server comes back. Attached terminals and live syncs do
not - `aether attach` and `aether sync --live` drop and reconnect, the same
as a client-side update.

**In the dashboard.** An admin does the same from the server prompt in the
**Updates** dialog:
**Update now** asks to confirm, naming how many runs are active first, and
**Update when idle** records the pending update and leaves a **Cancel**
button in its place. The prompt then follows the phases live - scheduled,
applying, restarting - and disappears once the server reports the new
version. A failure shows the server's own error and the two commands below.
Every phase is in the workspace activity feed as well, and a member who is
not an admin sees a one-line notice in the sidebar while an update is
scheduled or applying, so the restart does not look like an outage. See
[dashboard-frontend.md](dashboard-frontend.md#update-prompts).

On the documented unprivileged install (the server binary's directory not
writable by the server process, see [First boot](#first-boot)), `--status`
reports that the server cannot update itself and `server update` refuses.
The dashboard's prompt offers no buttons there either: it names the same
reason and these commands, with a copy button. Run them on the server host:

```sh
sudo aether update
sudo systemctl restart aether-server
```

**The desktop app is separate.** The dashboard ships inside the CLI, so
updating the CLI updates the dashboard. The Electron shell around it - window
chrome, notifications, `aether://` deep links - is whatever `aether gui build`
last produced, and records the full version and executable path of the CLI
that built it. Both `aether update` and the dashboard's **Update now** rebuild
it for you; the "desktop app is out of date" prompt is what is left when that
rebuild was skipped (`--no-app`) or failed, or the shell deliberately uses another CLI through
`AETHER_BIN`. It is not tied to a release being available, because the usual
way to get there is to have just updated.

### Account share migration

A run on a shared agent account now uses its launcher's image and home and
mounts the agent's login from the account owner's home, plus the owner's
agent installation, read-only, when the launcher has none
([security.md](security.md#account-sharing)). Recipients connect their own
GitHub (**Connect GitHub** in the local dashboard's onboarding, or `aether
github connect`); they no longer get the owner's image, GitHub login, or
other files. Runs on a shared account, and every container
that mounts a sharing owner's home (their runs, their environment,
and candidate verification started by them or by their runs), need Docker
Engine 26.0 or newer; on an older engine Aether refuses them with `runtime:
docker engine API "1.44" cannot mount a path beneath a member home; that needs
API 1.45 (Docker Engine 26.0) or newer`. A member-defined agent no longer
launches on a shared account. Containers created before the upgrade still
mount the owner's whole home until they end, and reopening one is refused;
stop shared runs before or after upgrading to end that exposure immediately
(**Kill run** in the run header's **More** menu, or `aether kill <run-id>`).
A member who already shares stops and reopens their environment once after the
upgrade, so a Claude Code login refreshed there reaches recipients' runs:
**Stop environment**, then **Open**, on the dashboard's **Environment** page, or
`aether terminal stop`, then `aether terminal`.

## Manual install

Every release publishes bare binaries, the Windows desktop app, and
`checksums.txt`:

```
aether-server-linux-amd64          aether-server-linux-arm64
aether-edge-linux-amd64            aether-edge-linux-arm64
aether-linux-amd64                 aether-linux-arm64
aether-darwin-amd64                aether-darwin-arm64
aether-windows-amd64.exe           aether-windows-arm64.exe
aether-desktop-windows-amd64.zip   aether-desktop-windows-arm64.zip
```

`aether-server` and `aether-edge` are Linux-only. The Windows and macOS assets
are the client. The `aether-desktop-windows` zips hold the unpacked Windows
desktop app that `aether gui build` installs ([Desktop app](#desktop-app)).
`aether-edge` is only for running your own edge ([edge.md](edge.md)); servers
and clients do not need it. It also ships as
the image `ghcr.io/3xdevops/aether-edge:<release-tag>`
([edge.md](edge.md#in-a-container)).

**Linux and macOS.** Download the one you want, check it against
`checksums.txt`, `chmod +x`, and drop it on your `PATH` under the name
`aether` or `aether-server`.

**Windows.** In PowerShell, from the directory you downloaded into:

```powershell
# 1. Verify. Compare this against the matching line in checksums.txt.
Get-FileHash -Algorithm SHA256 .\aether-windows-amd64.exe

# 2. Put it somewhere on PATH under the name aether.exe.
$dir = "$env:LOCALAPPDATA\Programs\Aether"
New-Item -ItemType Directory -Force -Path $dir
Copy-Item -Force .\aether-windows-amd64.exe "$dir\aether.exe"

# 3. Add that directory to your user PATH (once), then open a new terminal.
[Environment]::SetEnvironmentVariable(
  "Path", "$([Environment]::GetEnvironmentVariable('Path','User'));$dir", "User")
```

Use `aether-windows-arm64.exe` on an Arm device. Confirm the installed CLI with
`& "$dir\aether.exe" version`. This manual copy is CLI-only: to install or
repair the desktop, use `v0.5.1-alpha.4` or newer and run
`& "$dir\aether.exe" gui build`. The explicit path ensures that the desktop
records this CLI rather than a different `aether` on `PATH`.
To upgrade, rerun the PowerShell installer or replace the CLI manually;
there is no `aether update` on Windows.

To install the desktop app from a zip you downloaded, verify it the same way,
then hand it to the CLI. `gui build` itself checks only the zip's layout, not
its origin:

```powershell
Get-FileHash -Algorithm SHA256 .\aether-desktop-windows-amd64.zip
& "$dir\aether.exe" gui build --zip .\aether-desktop-windows-amd64.zip
```

### Recovering commands after a Windows desktop build

Older builds installed the desktop app into the CLI's own
`%LOCALAPPDATA%\Programs\Aether` directory, replacing its contents. Windows
treats Electron's `Aether.exe` and the CLI's `aether.exe` as the same name.
Afterwards every `aether` command could launch Electron, which tried to
launch itself as the gateway and printed
`aether gui printed an unparseable line: SyntaxError: Unexpected end of JSON input`.
This is an installation collision, not a Defender detection or damaged
linked-server configuration.

Quit Aether, including any stuck copies in Task Manager. Download a client
release `v0.5.1-alpha.4` or newer and verify it against that release's
`checksums.txt`, as above. From that download directory, restore the CLI
and rebuild the app:

```powershell
$cli = "$env:LOCALAPPDATA\Programs\Aether\aether.exe"
Copy-Item -Force .\aether-windows-amd64.exe $cli
& $cli version
& $cli gui build
Get-Command aether -All | Select-Object Source
aether version
```

Use the Arm asset on an Arm device. If you installed the CLI elsewhere, set
`$cli` to that path. The explicit path bypasses a shadowing command on `PATH`;
`Get-Command` shows which copy a bare `aether` invokes. An older CLI can restore
terminal commands, but running its `gui build` repeats the collision.

The rebuilt app replaces the Start Menu shortcut and uses
`%LOCALAPPDATA%\Programs\Aether Desktop\aether-desktop.exe`. The old
`Programs\Aether` directory is deliberately not deleted: it holds the restored
CLI and may hold unrelated files. Your `%APPDATA%\aether\config.json` and
`%USERPROFILE%\.ssh` files do not need changing.

### Windows Start Menu shortcut diagnosis

A successful `aether version` proves only that a CLI is present. `-Role none`
and a manual binary copy do not build the desktop or create its shortcut;
they also do not remove an older desktop. The default installer runs
`gui build`; a successful build installs the separate desktop executable and
registers the current user's Start Menu entry.

Inspect the real Programs folder and existing link without creating a link:

```powershell
$programs = [Environment]::GetFolderPath([Environment+SpecialFolder]::Programs)
if ([string]::IsNullOrWhiteSpace($programs)) { throw "Windows did not resolve the Programs folder" }
$link = Join-Path $programs 'Aether.lnk'
$desktop = "$env:LOCALAPPDATA\Programs\Aether Desktop\aether-desktop.exe"
$programs
Test-Path -LiteralPath $desktop -PathType Leaf
Test-Path -LiteralPath $link -PathType Leaf
if (Test-Path -LiteralPath $link -PathType Leaf) {
  $shell = New-Object -ComObject WScript.Shell
  $shortcut = $shell.CreateShortcut($link)
  $shortcut | Select-Object FullName, TargetPath, Arguments, WorkingDirectory, IconLocation
}
```

The target should be the desktop executable above, not the CLI `aether.exe`.
If it is missing or stale, close Aether, then rebuild using the exact installed
CLI (adjust `$cli` for a custom installation):

```powershell
$cli = "$env:LOCALAPPDATA\Programs\Aether\aether.exe"
& $cli version
& $cli gui build
```

Use a current release containing the Programs-known-folder fix when repairing
redirected folders; the minimum desktop-compatible release alone does not
imply it contains every later shortcut fix. A build with this fix prints
`launcher <resolved path>` so you can compare its destination with `$link`.
Inspect the link again, then open **Aether** from Start.

If it still fails, include the CLI version and explicit path, full installer
or `gui build` output (including any native Windows error and launcher path),
resolved `$programs`, link properties, whether the desktop executable exists,
Windows version/architecture, and the exact launch/security warning in an
issue. Redact personal path components as needed. A missing entry alone is
not proof of Defender quarantine; check Protection History for a detection.

### Windows Defender and SmartScreen

The released CLI is unsigned, and building the desktop locally does not give
it a trusted publisher signature. Either can trigger download reputation,
SmartScreen, antivirus, Smart App Control, or organization policy. Inspect
the exact warning, file path, publisher/signature, and applicable organization
policy before deciding what happened; these are not interchangeable systems.

| What you see | Possible source | What to inspect |
| --- | --- | --- |
| Browser download blocked or flagged | Edge uses Microsoft Defender SmartScreen; Chrome uses Google Safe Browsing, and other security tools may also intervene | Browser's exact warning and download details; follow organization policy |
| "Windows protected your PC" on launch | Windows SmartScreen reputation check | Named application and publisher, signature details, and organization policy |
| File disappears or "virus detected" | Microsoft Defender antivirus or another antivirus product | Protection History/security product report, detection name, and affected path |
| "Smart App Control blocked an app that may be unsafe", or the CLI fails with "An Application Control policy has blocked this file" | Smart App Control, which runs only signed apps or apps Microsoft's cloud already trusts | **Windows Security > App & browser control > Smart App Control**. The installer cannot and will not bypass it; signed releases are the fix |

Do not disable protection, add exclusions, or restore/run a quarantined file
to work around a warning. A matching checksum establishes download integrity,
not safety or publisher trust. Follow the report process below; managed
devices may require an administrator-approved, signed distribution.
The installer does not alter execution policy, Smart App Control, or
organizational security policies.

**Report a detection.** Verify the SHA-256 against `checksums.txt` first - if
it does not match, do not run the file and open an issue. If it matches,
submit it at
[microsoft.com/wdsi/filesubmission](https://www.microsoft.com/en-us/wdsi/filesubmission)
as a software developer. Microsoft clears confirmed false positives through a
definition update, which fixes it for everyone on that release. Please open an
issue with the detection name too, so the release notes can carry it.

**Windows build safeguards.** Windows binaries carry a VERSIONINFO resource,
an icon, and an application manifest declaring `asInvoker`. Browser launch
uses Windows APIs; Start Menu registration uses `IShellLinkW` and `IPersistFile`
instead of a shell command. Neither operation launches `powershell.exe`,
`cmd.exe`, or `rundll32.exe`.

These measures do not confer publisher trust. Releases remain unsigned;
Defender or SmartScreen may still flag a new binary.

Release publication is gated on a Defender scan of the exact final x64 and
ARM64 CLI binaries and desktop zips, downloaded from the build artifacts
without rebuilding. Each zip is scanned whole and extracted. The gate records
SHA-256 hashes and definition/protection evidence, checks effective
real-time/cloud/sample-submission settings, and rejects missing or changed
files, scan errors, or new detections (including remediated ones). That
verdict covers those bytes and scan conditions, not signing, reputation, a
desktop built from source, or a guarantee of warning-free execution on a
different machine.

## The Windows client

Windows runs the client only. There is no `aether-server` for Windows and
there will not be one: every run is a Linux container on a Linux host.

Where the client keeps its state:

| What | Path |
| --- | --- |
| Linked-server config | `%AppData%\aether\config.json` |
| Host-key trust store | `%USERPROFILE%\.ssh\known_hosts` |
| Default private key | `%USERPROFILE%\.ssh\id_ed25519` |

**SSH agent.** The client talks to the Windows OpenSSH agent over its named
pipe, `\\.\pipe\openssh-ssh-agent`, so a passphrase-protected key works the
same way it does on Linux. The agent is a Windows service that is not running
by default:

```powershell
Get-Service ssh-agent                       # is it running?
Start-Service ssh-agent                     # start it now (needs admin)
Set-Service ssh-agent -StartupType Automatic
ssh-add $env:USERPROFILE\.ssh\id_ed25519
```

`SSH_AUTH_SOCK` takes precedence when it is set: the client dials it as a unix
socket rather than using the pipe. Leave it unset unless you deliberately run
a different agent. When neither is reachable the client falls back to the key
file rather than failing; only with no usable key either does `aether link`
report `attempted methods [none]`, and that error names the key file it found
and why it could not use it. `aether link <addr> --key <path>` picks a key
outside `%USERPROFILE%\.ssh\id_ed25519`.

**Console.** `aether attach` mirrors an agent's TUI byte for byte, so the
console needs ANSI escape processing. The client enables it on the console it
writes to and restores the previous mode on exit. Windows Terminal and current
conhost handle it; a console that refuses is a cosmetic degradation, not a
failed attach.

**Not available on Windows**, by design rather than oversight:

- `aether init` refuses to run. It prepares a Linux server's data directory,
  so run it on the server box.
- `aether update` refuses to run. Re-download the release binary instead.
- `scripts/install.sh` is a POSIX shell script. Use `scripts/install.ps1` instead.
- `aether-server` itself. Point the client at a Linux server.

Everything else is the same client: `link`, `run`, `attach`, `gui`,
`pull`, `daemon`, and the rest.

## Building from source

Needs Go 1.26+, GNU make, and Bun 1.3+ (the server embeds the dashboard SPA, so
the web build runs first).

```sh
git clone https://github.com/3xDevOps/Aether
cd Aether
make build      # dashboard SPA, then both binaries into dist/
```

See [CONTRIBUTING.md](../CONTRIBUTING.md) for the rest of the toolchain.

## Desktop app

Optional: an Electron shell that launches `aether gui` for you and shows the
dashboard in its own frameless window, with a notification that names a run
when it needs attention and opens it when clicked, a needs-attention badge,
and deep links: `aether://run/<id>` opens a run, and an edge invitation's
`aether://join?…` and setup's `aether://claim?…` open onboarding's Connect
step to join or claim ([edge.md](edge.md#the-dashboard)). The notification shows only while the app
is open; a phone gets one through the server-hosted dashboard
([networking.md](networking.md#notifications)). It is the same SPA
with the same full SSH authority, just without a browser tab to lose. On
Windows the CLI downloads the prebuilt app its release publishes; on Linux and
macOS it builds the app on your machine. Neither needs anything installed
first. The Windows installer installs it by default; on Linux and macOS,
answering `client` to the install script's question does the same.
This is the command by hand. The window opens at 1280 by 840 and stops
at 960 by 600, the smallest size the dashboard's own layout holds.

```sh
aether gui build
```

On Linux and macOS, cancelling the build with SIGINT exits 130; SIGTERM exits
143. Ordinary build failures exit 1 and print the build's original error.

On Windows, a release CLI fetches its release's signed `aether-release.json`,
downloads `aether-desktop-windows-amd64.zip` (or `-arm64`) into
`%LOCALAPPDATA%\aether\desktop-build` (`--build-dir` overrides), checks its
size and SHA-256 against the manifest, and extracts it into the install
location below. Nothing is built, and nothing but the app's own files is
written. The download is about 100 MB and is deleted after the install.
While it runs, the command prints a line such as `  42 of 104 MB (40%)` every
5 seconds. If the manifest cannot be fetched or the zip fails its check, the
command fails with that error and names `aether gui build --source`. Three cases build from
source instead:

- `aether gui build --source`, for a machine that should build its own app.
- A development build of the CLI (`aether version` prints `dev` or a `git
  describe` version), which has no release to download from.
- A release that publishes no desktop zip. The CLI prints
  `release <tag> does not publish aether-desktop-windows-<arch>.zip; building
  the desktop app from source` and carries on.

`aether gui build --zip <path>` installs a desktop zip you already have, for
an offline machine or a CI smoke test. No signed manifest vouches for a local
zip, so the CLI checks only that it is a Windows desktop app
(`aether-desktop.exe` at its root); verify the zip against the release's
`checksums.txt` first.

### Building the desktop app from source

On Linux and macOS, and on Windows in the cases above, the CLI unpacks the
shell sources it carries into your cache directory
(`~/.cache/aether/desktop-build` on Linux,
`~/Library/Caches/aether/desktop-build` on macOS,
`%LOCALAPPDATA%\aether\desktop-build` on Windows; `--build-dir` overrides), runs
`npm install` there to install exactly the Electron and electron-builder
versions `desktop/package-lock.json` pins, runs electron-builder, and installs
the result. Either way the app lands where your desktop lists applications:

| OS | App | Launcher |
| --- | --- | --- |
| Linux | `~/.local/share/aether/desktop/` | `~/.local/share/applications/aether-desktop.desktop` |
| macOS | `/Applications/Aether.app` | Applications folder and Spotlight |
| Windows | `%LOCALAPPDATA%\Programs\Aether Desktop\` | Start Menu > Aether |

A macOS account without administrator rights cannot write to `/Applications`,
so the app goes to `~/Applications` instead; the command prints where it put
it.

The Windows desktop executable is `aether-desktop.exe`. Keep the CLI's
`aether.exe` in its own directory (`%LOCALAPPDATA%\Programs\Aether` in the
manual install above), never inside the desktop app directory: a rebuild
replaces that directory in full. A build does not change your `PATH`.

The build uses `node` and `npm` from `PATH` when `node` is version 22
or newer. Otherwise it downloads a pinned Node.js 22 release for this OS and
CPU (Linux, macOS and Windows, x64 and arm64) from <https://nodejs.org/dist/>,
verifies it against that release's `SHASUMS256.txt`, and unpacks it in a
directory named for that version beside the build directory
(`~/.cache/aether/node/` on Linux, `~/Library/Caches/aether/node/` on macOS,
`%LOCALAPPDATA%\aether\node\` on Windows). That copy is on `PATH` for this
build's `npm install` and electron-builder only; nothing else on the machine
changes and no shell profile is edited. So the first build needs network
access, and later builds reuse the cached copy; a build that fetches a newer
pinned version deletes the old one. A failed download or a checksum mismatch
fails `aether gui build` with the error and the URL to fetch by hand; it
never falls back to a system Node older than 22. The Node.js download prints
the same progress lines as the desktop zip, and a source build prints one
line as each step starts: unpacking, the Node.js check, `npm install`,
electron-builder, and installing.
The CLI runs electron-builder through Node directly, avoiding `npx` shell
wrappers and their handling of spaces and metacharacters in Windows paths.

The first build downloads the Electron runtime (about 100 MB) into
electron-builder's own cache (`~/.cache/electron` and
`~/.cache/electron-builder` on Linux, `~/Library/Caches/electron` and
`~/Library/Caches/electron-builder` on macOS, `%LOCALAPPDATA%\electron\Cache`
and `%LOCALAPPDATA%\electron-builder` on Windows), and `node_modules` stays
in the build directory, so rebuilding is quick.

### Managing the installed app

Run `aether gui build` again to replace an installed app; on macOS it also
removes an older copy from the other Applications folder. The new app is
staged beside the installed one and swapped in with a rename, so an app that
is running while you rebuild it keeps working until you restart it - deleting
its files under it would take the window down. Windows still holds a running
program's files open, so close the Aether window there first. To remove
everything, delete the two paths in the table, the `aether` cache directory
(the build directory and the private Node copy), and, after a source build,
those caches.

`aether gui build --json` prints one JSON line per phase on stdout and leaves
the build's own output on stderr, which is how the dashboard follows a rebuild
it started. A build from source reports:

```json
{"phase":"unpacking"}
{"phase":"fetching node"}
{"phase":"installing dependencies"}
{"phase":"packaging"}
{"phase":"installing"}
{"phase":"done","path":"/home/you/.local/share/aether/desktop"}
```

A Windows install of the prebuilt app reports `downloading`, then
`installing` and `done`; `--zip` reports `installing` and `done`. A release
without the zip reports `downloading` and then the source build's phases.

A failure ends with `{"phase":"error","error":"..."}` carrying the build's own
message, and the command still exits non-zero.

The app requires the `aether` CLI installed first; it does not bundle the
binary. As a build-time check, `aether gui build` requires an installed CLI
discoverable through `AETHER_BIN`, then `PATH`, then the installer defaults:
`%LOCALAPPDATA%\Programs\Aether` on Windows, `/usr/local/bin` and
`~/.local/bin` on Linux and macOS. The install records the path of the CLI
that ran it in the app's `resources/aether-cli.json` (on macOS, which keeps
the bundle untouched after signing, in the app's `package.json` at build
time). At launch, `AETHER_BIN` overrides that path; otherwise the
shell starts the recorded executable, not another copy on the desktop
session's `PATH`. If that executable is gone or cannot run, the error names
its exact path: restore it, set `AETHER_BIN` to a working CLI, or rerun that
installed CLI's `gui build` command. Shells without a recorded path search
`PATH` and the installer defaults. This CLI selection is
the launcher's job alone: once the app is running, the dashboard's agent
detection and scans widen `PATH` from your login shell each time they look,
so coding agents installed through a shell profile are found from the
application menu too, and "Check again" picks up a fresh install without a
relaunch. The probe runs `$SHELL -l -i` with `AETHER_RESOLVING_PATH=1` set,
so a shell rc file can skip work meant for a real terminal (an `exec tmux`,
a prompt) when that variable is set. Windows apps already get your user
`PATH`, so nothing changes there.

On Linux the launcher passes `--no-sandbox`, the same default electron-builder
gives its AppImages: an unpacked Electron cannot use its SUID sandbox helper
without root, and Ubuntu 24.04+ denies the namespace sandbox to unconfined
binaries. The renderer still runs with context isolation and no Node access,
locked to the loopback gateway.

**The dashboard ships inside the CLI, not inside this app.** The SPA is
embedded in the `aether` binary (`web/embed.go`) and served by `aether gui`, so
a dashboard change reaches the window only when the CLI is rebuilt and
reinstalled - not when the desktop app is rebuilt:

```sh
make build && sudo install -m 0755 dist/aether /usr/local/bin/aether
```

If the window renders an older dashboard than your checkout, check the
version of the CLI recorded by the shell (or the `AETHER_BIN` override):
`<path-to-aether> version` prints the commit it was built from. A newer
`aether` on your terminal's `PATH` does not change the shell's CLI.
Building installers (`.dmg`, `.exe`, AppImage) from a checkout and code signing
are in [CONTRIBUTING.md](../CONTRIBUTING.md#desktop-shell).

## Android app

Optional: an app that opens the dashboard full screen on a phone. Every
release carries two builds of it in its assets and in `checksums.txt`:
`aether-android.apk` for installing straight from the release page, and
`aether-android.aab`, the app bundle Google Play takes. It is not on Google
Play yet; the listing material is in `android/listing/`, and the Play link
replaces this sentence the day the listing goes live. What the app stores and
sends is in [privacy.md](privacy.md); the licences it ships under are in
[notices.md](notices.md).

1. Open the release page in the phone's browser and download
   `aether-android.apk`. Check it against its line in `checksums.txt` if you
   want to.
2. Android asks once for permission to install apps from that browser. Allow
   it, then open the downloaded file.
3. The first screen asks how to reach your server. Pick one of two modes.

**Tailscale.** The app is a WebView locked to your server's HTTPS dashboard
origin. Identity is the phone's own tailnet login, resolved by the server on
every request ([networking.md](networking.md#the-dashboard)). Two things have
to be true first: the server has `web-port` set, and the phone is signed in
to the same tailnet. Type the server's MagicDNS name, for example
`my-server.tailnet-name.ts.net`. A pasted
`https://my-server.tailnet-name.ts.net/` works too, and a port other than 443
goes on the end: `my-server.tailnet-name.ts.net:8443`. `http://` is refused
rather than upgraded. The dashboard opens at the board, already identified;
there is no sign-in. The app stores only that address.

**Aether Edge.** Sign in with GitHub; no Tailscale needed. The APK carries the
`aether` CLI built for Android (`arm64-v8a` and `x86_64`) as
`lib/<abi>/libaether.so`. The app runs `libaether.so gui --json --port <port>`, the
[local gateway](local-gateway.md) the desktop app runs, and loads its tokened
`http://127.0.0.1:<port>/?token=...` URL in the WebView. The WebView stays
locked to that origin, port included; every other link opens in the phone's
browser, and cleartext is permitted only for `127.0.0.1`. The dashboard's own
onboarding then signs you in through the [edge](edge.md), the same flow as the
[desktop app](#desktop-app):

1. The Connect step shows a sign-in code and opens the verification page in the
   phone's browser. Sign in with GitHub there.
2. The step lists the servers the edge says you can reach. Pick one.
3. It shows the server's id and host key fingerprint. **Link and pin** saves
   the link only once you confirm.
4. Where the server requires device approval (`edge-access
   approved-devices`), the step waits, retrying every 5 seconds, until an
   admin approves the device ([teams.md](teams.md#through-an-edge)). A refusal
   is shown as the server or edge sent it.

SSH still runs end to end from the phone to the server; the edge relays
encrypted bytes, and the server's access control is unchanged.

In Edge mode the app stores, under its private files directory,
`.config/aether/config.json` (the links), `edge-device-key` and
`edge-tokens.json` (both mode `0600`) and `edge.lock`, plus `resolv.conf`.
All of it is excluded from cloud backup and device transfer
(`allowBackup="false"` and `data_extraction_rules.xml`) and deleted on
uninstall. Uninstalling or clearing the app's storage loses the device key:
the phone then signs in again as a new device and needs approval again where
the server requires it.

`resolv.conf` holds the phone's current DNS servers. Go's resolver finds no
`/etc/resolv.conf` on Android, so the app reads the active network's DNS
servers (permission `ACCESS_NETWORK_STATE`), rewrites the file on every
network change and points the CLI at it with `AETHER_RESOLV_CONF`
([networking.md](networking.md#android-dns)). The CLI is given Android's
system CA store (`/apex/com.android.conscrypt/cacerts` on Android 14+, else
`/system/etc/security/cacerts`) and does not trust user-installed CAs, as the
app's network security config does not.

The gateway port is fixed per install, so the dashboard keeps its settings
across gateway restarts. The WebView does not cache the gateway's pages and
runs no service worker, and the page is blanked (`about:blank`) whenever the
gateway exits, until the restarted gateway prints its new URL. Pages the
dashboard opens in a new window, such as the sign-in verification page, open
in the phone's browser.

**Updates.** The app never installs an update silently: you open the APK
it links to, or update it in Google Play, and Android's installer asks you to
confirm. In both modes, when the app comes to the front and its next check is
due, about an hour after the last one (±10%, so phones do not check in step),
it runs the bundled CLI's `aether update --check --json` in the background,
so the release lookup and version order are the desktop's. **Check for
updates** on the Connection screen (long-press the launcher icon, or
**Connection** on a failed load) runs one now and always answers: `You're up
to date (v0.8.0). Checked 14:05.`, the newer version with its button, or the
CLI's error verbatim; a result saved before the app was last installed or
updated is not shown. A background check that fails stays quiet until the
last answer is 24 hours old; from then on the banner shows `Could not check
for updates: <error>` as one muted line until a check succeeds or you dismiss
it.

When a newer release is out, a banner over the top of the dashboard names the
new version and the one you run. Its buttons depend on who signed the app and
which installer Android recorded (`getInstallSourceInfo` on Android 11 and
newer, `getInstallerPackageName` before):

- Installed by Google Play, or signed with any key but the release key:
  **Open Google Play** opens the app's listing in the Play app
  (`market://details?id=io.aether.android`), or in the browser when Play is
  missing. Play App Signing re-signs what Play serves, so a Play build
  installed through another store or as sideloaded split APKs still updates
  from Play. GitHub publishes a release before Play has reviewed and rolled
  it out, so this banner shows only once the app has been behind a release
  for 7 days; newer releases in that week do not restart the wait.
- Installed any other way and signed with the release key, such as the APK
  from a release page: **Download update** opens
  `https://github.com/3xDevOps/Aether/releases/download/<tag>/aether-android.apk`
  in the browser. Open the download; Android asks to confirm, checks that it
  is signed with the same key as the installed app, and installs it over the
  app with its data. The release key's certificate SHA-256 is
  `2d7e0777aa8a24ac0090dcdd4c620910c9ef13c7775b1576ed69230f1dd37c3a`, as
  `apksigner verify --print-certs aether-android.apk` prints it. A `make
  android-debug` build is signed with another key, so Android will not
  install the release APK over it, and its banner points at Google Play.

Both carry **Release notes**, which opens the GitHub release. If a page does
not open, the banner shows Android's own error. Dismissing the banner
silences that version only, on this phone; the next release shows it again.

**Update gate (Edge mode).** The server refuses a client older than its own
release (`-32005`, HTTP 426 through the gateway). In Edge mode the bundled
CLI is that client, so the app asks the gateway's `update.check`
(`POST /local/v1/update.check`) when the gateway starts, on every resume,
whenever a dashboard page finishes loading (a refused WebSocket reports no
HTTP status), and whenever the dashboard gets an HTTP 426; the first check
after a 426 passes `refresh` so the gateway looks past its cached release
lookup. When it reports `client_update_required`, a native screen replaces
the dashboard and cannot be dismissed: `This app (v0.7.0) is too old for the
server (v0.8.0 needs v0.8.0 or newer).` **Download update** opens
`https://github.com/3xDevOps/Aether/releases/download/<tag>/aether-android.apk`,
or the Play listing for a Play install, with no 7-day wait. The tag is the
gateway's `target_version` (the server's release), else
`min_client_version`, and never one older than `min_client_version`, which
Android would refuse as a downgrade. GitHub's latest release is not used: it
can be newer than the server, which refuses a newer client too. The URL is
printed on the screen to copy. While the screen is up the dashboard is
unloaded, so it stops calling the server. Installing the update restarts the
app, whose new gateway answers that no update is required, so the dashboard
comes back. A check whose answer has no `server_version` (the gateway could
not reach the server; `server_error` says why) leaves the screen as it was
and shows the error under it. **Check for updates** asks again, with
`refresh`; **Connection** opens the Connection screen, which switches between
Aether Edge and Tailscale (choosing another Edge server happens in the
dashboard, after updating); **Copy details** puts the versions and the
download URL on the clipboard. A dev build or a version that is not a release tag is never
gated. Tailscale mode has no gate: the server hosts that dashboard, so it is
always the server's own version, and only the banner applies to the app.

The app does not hold `REQUEST_INSTALL_PACKAGES`, and the CLI never replaces
itself (`AETHER_PACKAGED` is set to `Android app`): `aether update` without
`--check` and the gateway's `update.apply` refuse with `this aether is part
of the Android app, which installs its updates; update the Android app
instead`. The gateway also omits the verbs that need a git checkout, a
service manager or local agents
([local-gateway.md](local-gateway.md#get-apiv1capabilities)).

If the gateway exits, the app restarts it after 1 s, 2 s and 4 s. After three
failed restarts it shows a native screen with the exit code and the CLI's
stderr verbatim, with **Retry** and **Connection**. A build without the
library (a bare Gradle build, see
[CONTRIBUTING.md](../CONTRIBUTING.md#android-shell)) says so and names the
missing path. The app stops the gateway when its window is destroyed and
restarts it on resume if it died.

Android may reclaim the dashboard's renderer process while the app is in the
background, for example while you confirm a sign-in code in the browser. The
app reloads the dashboard, on the page you had open, when you return to it.
Any other renderer loss shows
a screen with **Retry** and **Connection** instead of reloading into the same
failure: `Android stopped the dashboard's renderer` for one killed while on
screen, `The dashboard's renderer crashed` for a crash. On Android 10 and
newer, a renderer that WebView reports unresponsive twice in a row (at least
5 seconds apart) is ended instead of freezing the app into an "isn't
responding" dialog, and the app shows `The dashboard's renderer stopped
responding and was ended`. A failed gateway's screen stays in front of these.
`adb logcat -s aether` shows each renderer loss.

**Multiple servers.** In Edge mode, Settings > This computer lists **Saved
servers** (the default link plus named ones). **Switch** relinks the gateway in
place and the dashboard reloads. **Link another server** opens onboarding's
Connect step with the edge server picker, and works while the default server
is offline. From the default, a newly linked edge server becomes the default
and the previous edge default stays as a saved server named by its server id.
From a named server, the new server is added as another saved server, named
by its server id, and no existing server changes. The app starts on the server you last switched to
(`gui_server` in `config.json`). Port forwarding is not offered on the phone:
a forward listens on `127.0.0.1`, which every app on the phone can reach.

**Changing mode.** Long-press the app icon and pick **Connection**. A failed
load's button is also **Connection**. In Tailscale mode, an address that does
not resolve leaves the WebView's own error page on screen, which names what
actually failed. The first screen carries the link to [privacy.md](privacy.md),
the only place the app itself shows one.

**Deep links.** Both modes open `aether://run/<id>` on that run, the same link
the desktop shell handles. Edge mode also opens `aether://join?...` and
`aether://claim?...` at onboarding's Connect step, validated like the desktop
app ([edge.md](edge.md#the-dashboard)). Tailscale mode shows a message that
these open in Edge mode: its dashboard is served by a remote server, which must
not see another server's secret.

A WebView cannot receive push notifications, so the app shows none: for those,
install the dashboard from Chrome instead
([networking.md](networking.md#notifications)). The back gesture walks the
dashboard's history and then sends the app to the background instead of
closing it, so a terminal keeps its scrollback. Links that leave the dashboard,
such as an agent's OAuth page, open in the phone's browser.

**Updates install over the old version**, because every release is signed with
the same key, and in Edge mode they replace the bundled CLI. An APK built from
a checkout is signed with a different key or not at all, so Android refuses it
as an update; uninstall first, which also drops everything the app stored,
including the Edge device key.

The app needs a WebView from Chromium 140 or newer to paint under the status
and navigation bars the way the dashboard expects. On an older one the app
pads for those bars itself, which costs the edge-to-edge look and nothing
else. WebView updates through the Play Store on every Android version, but
Chromium 139 dropped Android 8 and 9, so a phone on those stops at Chromium
138 and keeps the padded layout for good.

Building the APK from a checkout is in
[CONTRIBUTING.md](../CONTRIBUTING.md#android-shell).

## Server prerequisites

- **Linux.** Windows and macOS are client platforms.
- **Docker**, running, with the server's user able to reach its socket. Every
  member environment and run is a container. Agent installation happens in
  the member's environment. Runs on a shared agent account
  ([teams.md](teams.md#agent-accounts)), and every container that mounts a
  sharing owner's home - their runs, their environment, and
  candidate verification started by them or by their runs - need Docker
  Engine 26.0 or newer (API 1.45);
  `docker version --format '{{.Server.Version}}'` prints yours.
- Optionally **Sysbox**, which lets agents run Docker inside their run
  containers. `aether-server setup` installs it on Ubuntu and Debian when you
  accept the question; on an existing server run
  `sudo aether-server sysbox install`. See
  [environments.md](environments.md#docker-inside-runs).
- **git** on the host. Bare repos, run checkouts, and diffs are real git.
- **`ssh-keygen`** on the host, from the OpenSSH client package
  (`openssh-client` on Debian and Ubuntu). Git uses it to sign the commits
  Aether makes at the end of a run once a member has connected GitHub; see
  [environment-home.md](environment-home.md#connect-github). `aether github
  connect` refuses rather than generating a key it could not sign with.
- Optionally **Tailscale**, which is the recommended way to make the SSH port
  reachable and the recommended identity layer. Without it, outbound HTTPS to
  the edge is enough: nothing needs to reach the server. See
  [networking.md](networking.md).

A standard headless Ubuntu server is sufficient. No desktop login, display
server, `DISPLAY`, X11, Wayland, Xvfb, host Node.js, host Chromium, or host
browser libraries are required. The companion image contains Chromium,
Playwright, their matching OS libraries, and fonts. The optional desktop app
is a client, not a server prerequisite.

## Images and containers

An image is a read-only package used to create containers. A container is one
runtime instance of that image. The server opens terminals only inside
containers, never on the host, and never mounts the Docker socket into a
workspace container. With Sysbox installed, a container instead runs its own
Docker daemon ([environments.md](environments.md#docker-inside-runs)).

Every container a member receives - agent runs, workspace shells, and their
environment - starts from that member's saved image. When the member
has not saved one, the server uses its standard image, configured with
`--standard-image`. See [environments.md](environments.md) for the standard
image contents, saving, resetting, and missing-image behavior.

The environment is where members install system packages and
toolchains. Files in the member home persist across containers. Files outside
the home live in the container layer and reach later runs only after the
member saves the environment.

Workspace creation accepts the workspace name and optional base branch:

```sh
aether workspace init <name>
aether workspace init <name> --base <branch>
```

Workspace variables and the setup script remain workspace settings and still
apply to runs; set them with `aether workspace env`
([environments.md](environments.md#workspace-environment)).

### Headless browser companion

The remote browser is a separate, lazy container, not part of the member's
saved environment. A release binary defaults to
`ghcr.io/3xdevops/aether-browser:<exact-release-version>`; development, dirty,
and git-describe builds default to the local `aether/browser:test` image.
There is no `latest` fallback. A versioned tag, registry digest
(`registry/image@sha256:...`), or locally loaded immutable image ID selects
the image; the runtime resolves a tag or digest to an immutable Docker image
ID before creating a companion. A missing registry image is pulled; a missing
local immutable ID is an error, not a request to substitute another image.

Set the image with `--browser-image`, the persisted `browser-image` config
key, or `AETHER_BROWSER_IMAGE`, in that order of precedence, followed by the
build default. For example:

```sh
sudo aether-server config set browser-image aether/browser:test
sudo systemctl restart aether-server
```

That local image must first exist in the server's Docker daemon. For an
environment-variable override in the installed service, put
`AETHER_BROWSER_IMAGE=<reference>` in `/etc/aether/aether-server.env` and
restart; a persisted config value still takes precedence. Explicit
`setup`/`install --browser-image` values are persisted, while an omitted
option follows the default. An existing config remains operator-owned across
upgrades; use `config show` to inspect the effective value.

A companion keeps the image it was created from until its run stops. After
a server upgrade, or after `make browser-image` on a development build, a run
whose browser was already open still has the old companion: stop the run and
reopen it, and the next browser open creates a companion from the configured
image. A persisted `browser-image` or `AETHER_BROWSER_IMAGE` pins the image
across upgrades, so move the pin to the new version yourself. An operation
the old companion lacks is refused by name, for example `this run's browser
companion predates dev.browser.inspect`; nothing else about that browser
changes.

Merely starting a run does not allocate Chromium. Opening its browser or
rendering a terminal PNG lazily reserves an additional 1 CPU and 1 GiB of
memory **plus** a private 256 MiB `/dev/shm`; leave that capacity alongside
the run. Insufficient capacity refuses the browser operation rather than
weakening its limits.
The browser uses the run's network namespace to reach local app ports, but
has no checkout or member-home mount. See
[security.md](security.md#remote-development-and-browser-isolation).

Chromium runs headless as a non-root user with its namespace and seccomp
sandboxes enabled. Docker's stock AppArmor policy is retained. A sandbox
startup failure is fatal and its diagnostic is returned; inspect the Docker,
kernel, and custom AppArmor policy on that host and rerun `make browser-smoke`
after correcting it. Do not disable the sandbox, use a privileged/unconfined
container, relax host-wide user-namespace controls, or install Xvfb as a
workaround. A lost companion reports
`browser companion unavailable; explicit restart required`; reopening must
be explicit because its old authenticated browser session may be gone.

For broker clients, `dev.browser.status` returns the lifecycle state
(`not_started`, `running`, `paused`, `creating`, `session_lost`, or `unavailable`)
and the actual failure reason. An unavailable Chromium session reports
`available: false` and `running: false`, even when its companion container is
still running. Recovery is explicit: acquire the browser surface
using that status's `session_id`, then call `dev.browser.reset` with the
current control lease. If initial creation failed before establishing a
session, the returned `pending:<creation-key>` is an opaque recovery
incarnation, not a page or frame identity. After reset, obtain the new
session and page references; do not replay old input.

Browser status and input operations still verify the recorded companion's
ownership and health. Only lifecycle changes rewrite its journal.

**Release prerequisite:** a new GHCR package starts private. Before the first
server release can complete, a repository administrator must make the
`aether-browser` package public. The release workflow smoke-checks each
architecture natively, publishes their versioned manifest, then pulls both
`linux/amd64` and `linux/arm64` anonymously before releasing the server.
Missing or private images block that gate. This describes the publication
requirement, not a claim that the new package already exists or that a smoke
run has passed. Pull-request CI builds local images without registry writes.

### Git inside run environments

Managed selected-path commits use native prepared `git update-ref --stdin`
transactions inside the selected run environment. The standard image uses
Ubuntu 24.04's distribution Git; stock Git 2.43 supports the required
primitive. No custom source build or new Ubuntu host upgrade is required,
and no minimum version is asserted for other environments.

One dereferencing `update HEAD NEW OLD` gives Git the expected old object ID
for its native compare-and-swap. After the `prepare` acknowledgement, Aether
checks symbolic `HEAD` while Git holds both the `HEAD` and branch locks,
then commits or aborts within that transaction. A custom environment without
the required native support fails closed; unrelated repository reads do not
depend on this transaction.

Failures preserve native Git stderr. A missing protocol acknowledgement adds
`runrepo: native reference transaction did not acknowledge <phase>`, where
the phase is `start`, `prepare`, `commit`, or `abort`. A branch mismatch reports
`runrepo: symbolic HEAD changed: expected <branch>, found <branch>`; a detached
or unreadable `HEAD` reports
`runrepo: symbolic HEAD changed to a detached or unreadable reference while preparing commit`.
Inspect the actual diagnostic: lock contention, a changed branch, a native
hook veto, and unavailable transaction support are not interchangeable errors.
Do not treat every failure as a request to upgrade Git.

If a custom image lacks transaction support, install an appropriate native
Git package in the selected member environment and save it for future runs,
or use `aether env reset` to return to the standard image. Reset discards saved
image customizations. Stop and reopen your environment and start a new run;
saving or resetting does not change an already-running container. See
[environments.md](environments.md#git-in-run-environments).

Managed commits honor native signing, identity, and coauthor trailers but
report `hooks_run=false` for commit hooks; use native `git commit` in the run
terminal when those hooks are required. Native `reference-transaction` hooks
remain active, including their preparation veto. The selected-path and
post-commit index boundary are explained in
[security.md](security.md#managed-selected-path-git-commits).

## First boot

`aether-server setup` walks you through the install: it asks for the listen
address, data directory, and tailnet policy (Enter accepts each default),
and writes the systemd unit and the config file. On a host that already runs
tailscaled, and where tailnet connections are not required to carry a key, it
asks one more question -
`Dashboard HTTPS port on the tailnet (0 = off)`, defaulting to `443` on a
fresh config - which is how a phone on the tailnet reaches the dashboard
([networking.md](networking.md#the-dashboard)). Without tailscaled it turns
on the edge, the relay the Aether project runs at `https://edge.onaether.dev`
that lets clients reach the server over SSH with a GitHub sign-in
([edge.md](edge.md)), and prints what the edge can see and the command that
turns it off; with tailscaled it asks, defaulting to no. With the edge on
it asks who may reach the server through it, `1` (account access) or `2`
(approved devices), and repeats the question until answered
([edge.md](edge.md#access-policies)). Setup is the only thing that turns
the edge on: `edge-url` is empty unless the config file or a flag names an
edge, so an upgrade never enrolls an existing server.
The last question, unless Docker already has Sysbox, is `Let agents use Docker
inside their runs? This installs the Sysbox runtime (Ubuntu/Debian)`,
defaulting to yes; setup installs it after writing the files. On a host that
cannot run Sysbox it prints `Docker inside runs is unavailable on this host:
<reason>`; if the install fails it prints the error and
`sudo aether-server sysbox install`. Ctrl-C stops only the wait: under
systemd the installer keeps running as the `aether-sysbox-install` unit,
because stopping apt or dpkg midway can leave packages half-configured, and
setup says so. Either way setup completes.
With the edge on, setup then prints the server id and a claim link for the
Aether app, with the claim code inside it for `aether link --claim <code>`.
Setup ends on `next:`, after everything else: the command that starts the
service, then how to reach the server from your computer - the claim link or
`aether link --claim <code>` while the edge is on and the server has no owner,
otherwise `aether link <this-host>:2222` with the port from `addr`. On a
terminal these are bold orange; with `NO_COLOR` set, or output that is not a
terminal, they are plain text.
Answering `server` to the install script's question runs it for you; this
is the same command by hand.

```sh
sudo aether-server setup
```

For an unattended install, `aether-server install` writes the same files from
flags instead of questions - any serve option below is accepted, and options
you leave off keep tracking the binary's defaults across upgrades. The edge
stays off unless you pass `--edge-url`, which is refused without
`--edge-access account` or `--edge-access approved-devices`:

```sh
sudo aether-server install --addr :2222 --tailnet-auto-join
```

`install` also installs Sysbox, as setup does without the question, unless
you pass `--sysbox=false`. The flag is not a serve option and is not written
to the config file. It ends on the same `next:` as setup but issues no claim
code: with the edge on and no owner, step 2 is
`sudo aether-server edge claim-code`, which prints the claim link.

To install or check Sysbox on its own, on the server host:

```sh
sudo aether-server sysbox install
aether-server sysbox status
```

The installer also writes a systemd drop-in,
`/etc/systemd/system/sysbox-mgr.service.d/aether.conf`, that starts
`sysbox-mgr` with `--disable-inner-image-preload`. Without it, Sysbox copies a
container's inner `/var/lib/docker` into its writable layer on every stop.
Re-running the install on a host that already has Sysbox applies it; Sysbox
restarts only when no container is running.

`aether update` prints the install command after replacing `aether-server`
on a Debian or Ubuntu host whose Docker lacks Sysbox. It never runs it.

Neither command starts anything; both end on `next:`, whose first command
starts the service, so an install never restarts a live server behind your
back. Run through `sudo`, they put `sudo` before each `systemctl`; from a root
shell they print it bare. To start the service and follow its log:

```sh
sudo systemctl daemon-reload && sudo systemctl enable --now aether-server
sudo systemctl status aether-server
sudo journalctl -u aether-server -f
```

The unit runs the server as root and creates `/var/lib/aether` through
`StateDirectory=`. Root is deliberate: Docker socket access is already
root-equivalent on the host, and member images with a non-root user make the
server chown run checkouts to that UID, which needs `CAP_CHOWN`. The header
comment in the unit spells out how to run unprivileged instead, and what you
give up: every environment image must keep a root user. Git works in those
runs - the checkout stays owned by the server's user, and every run container
sets `safe.directory=/workspace` through `GIT_CONFIG_COUNT`, so neither the
agent's git nor the dashboard's Git panel stops at `detected dubious
ownership`. This needs git 2.31 or newer in the image; the standard image
qualifies. An unprivileged server cannot delete files a root agent created,
such as `.git/objects` or `node_modules`. When removing a finished run's
checkout or a removed member's home fails with `permission denied`, the server
empties the directory from a short-lived root container of the standard image
(`find /reclaim -mindepth 1 -delete`, the directory mounted at `/reclaim`) and
then deletes it. If that fails too, the journal logs `cannot remove files a
root container created` once per directory with both errors, and the directory
stays under the data directory until you remove it as root.

Browser support also needs permission to assign its private control directory
to UID/GID `1000:1000`. An unprivileged server that cannot do this cannot launch
the companion or render terminal PNGs. Keep the private directory permissions;
do not make the control socket world-accessible to work around ownership errors.

To run the server in the foreground instead - handy the first time - skip
setup and serve directly:

```sh
aether-server serve --data-dir /var/lib/aether --addr :2222
```

It prints one startup line naming what it serves:

```
aether-server <version> serving SSH on :2222 (data dir /var/lib/aether)
```

With `--web-port` set it also logs the dashboard's URL once that serves, or
why it does not ([networking.md](networking.md#when-it-does-not-start)).

Serve options, which are also the config-file keys. For duration options, `0`
uses the default; negative values have the semantics in the table.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--data-dir` | `/var/lib/aether` | Everything the server owns. |
| `--addr` | `:2222` | The SSH listener. This is the port clients must be able to reach. |
| `--web-port` | `0` (off) | Serve the dashboard over HTTPS on this host's tailnet addresses at this port; `443` makes it `https://<magicdns-name>/`. Needs tailscaled, MagicDNS and HTTPS certificates; see [networking.md](networking.md#the-dashboard). |
| `--standard-image` | `ghcr.io/3xdevops/aether-standard:<build-version>` | Standard image used for members who have not saved an environment. |
| `--container-runtime` | `auto` | OCI runtime for run and Environment containers: `auto` uses `sysbox-runc` when Docker has it registered, else the daemon default; `runc` opts out; any other value names a registered runtime. See [environments.md](environments.md#docker-inside-runs). |
| `--browser-image` | `ghcr.io/3xdevops/aether-browser:<exact-release-version>`; `aether/browser:test` for development builds | Lazy sandboxed browser companion. Explicit flag overrides persisted config, then `AETHER_BROWSER_IMAGE`, then the build default. |
| `--edge-url` | empty (off) | Edge the server enrolls with, for members without a direct or tailnet route. `aether-server setup` sets it to `https://edge.onaether.dev` on a host without tailscaled. See [edge.md](edge.md). |
| `--edge-access` | `approved-devices` | Who may reach the server through the edge: `account` (signing in is enough) or `approved-devices` (each new device waits until a person approves it). `install` refuses `--edge-url` without it. See [edge.md](edge.md#access-policies). |
| `--tailnet-auto-join` | off | Tailnet identities join approved instead of pending. |
| `--tailnet-require-key` | off | Tailnet connections must also present a registered SSH key; mutually exclusive with `--web-port`, whose browser cannot present a key. |
| `--conflict-coordination` | on | Let overlapping runs message each other; see [coordination.md](coordination.md). |
| `--agent-update` | on | Update a shipped agent installed in the member home before launching it; see [harnesses.md](harnesses.md#updates-before-launch). |
| `--stall-threshold` | `10m` | Silence after which a run parks needs-attention; see [failure-handling.md](failure-handling.md). |
| `--poll-interval` | `30s` | How often stalls are checked. |
| `--checkout-ttl` | `72h` | How long a finished run's worktree is kept. Negative disables the GC. |
| `--run-container-ttl` | `168h` (7 days) | Grace for the exact compute environment after a Standard or Enhanced run is explicitly closed, or a swarm worker completes. `0` uses the default; a positive duration overrides it; negative means immediate release. Interactive outcome reports leave the session open and start no expiry timer. Paused containers still consume RAM during grace. |
| `--min-free-disk` | `0` (automatic) | Free-byte reserve on the Aether data and runtime storage filesystems: 5% of each filesystem, at least 5 GiB and at most 20 GiB. A positive integer overrides the reserve in bytes; negative disables disk admission, not memory checks. Provisioning also needs a 1 GiB startup allowance. When free space on any of these filesystems falls below the reserve plus 4 GiB, Aether frees rebuildable data before it refuses a launch; see below. |
| `--run-cpus` | `0` (automatic) | CPU ceiling per run, member environment and candidate verification: up to 8 CPUs, clamped to the host CPU count. A positive number overrides it. |
| `--run-memory` | `0` (automatic) | Memory ceiling per run, member environment and candidate verification: 8 GiB. A positive integer overrides it in bytes, for example `17179869184` for 16 GiB. |
| `--run-pids` | `0` (automatic) | Process/thread ceiling per run, member environment and candidate verification: 4096. A positive integer overrides it. |
| `--agent-definitions` | none | Inline JSON custom agent definitions via this flag or `AETHER_AGENT_DEFINITIONS`; see [harnesses.md](harnesses.md). |

These generous resource limits are automatic: a 32 GiB-or-larger host does not
need routine budget settings to run a moderate swarm. They apply to new run,
member-terminal, agent-updater and candidate-verification containers; explicitly
supplied trusted verification budgets remain in force. The browser retains its
separate 1 CPU / 1 GiB limits. Negative or nonfinite run budgets are rejected by flags,
config edits and server startup. Config-file keys use the flag names without
`--`; `aether-server config set run-memory 17179869184` changes the ceiling for
new containers after the service restarts. Existing containers are not resized.

Before provisioning, Aether checks actual available host memory and free space
on its data filesystem and the runtime's verified storage filesystems, including
containerd content and snapshot storage when used. The disk reserve above is
kept in addition to a 1 GiB allowance for each concurrent provisioning. Memory
admission keeps the larger of 2 GiB or 10% of host RAM, plus 512 MiB for each
concurrent provisioning. These temporary allowances are released on success,
failure or cancellation. The 8 GiB ceiling is not an 8 GiB reservation: idle or
thinking swarm workers are not refused just because their maxima add up to more
than host RAM.
Candidate verification uses these same admission checks. Its temporary
provisioning allowance spans container creation/start, not the full command
lifetime; its runtime cache ownership lasts until confirmed destruction.

When free space on any checked filesystem falls below the reserve plus 4 GiB,
the scheduler frees rebuildable data, checking about once a minute and again
before refusing a launch. It re-measures after each step and stops as soon as
every filesystem is back above that line. A filesystem smaller than the
reserve plus 4 GiB never triggers it, because freeing data could not reach
that line. In order:

1. Paused containers of closed runs, oldest expiry first. The run gets a
   timeline note and can no longer be reopened; its branch, evidence and
   history are kept.
2. Finished run checkouts, regardless of a positive `--checkout-ttl`; a
   negative one keeps them. The usual safety checks apply: active runs,
   retained containers and uncaptured evidence keep their checkout.
3. Stopped environment terminal containers, which discards their unsaved
   environment changes.
4. Idle cache pools, obsolete saved images and legacy in-home caches. Legacy
   caches go even while the member has runs open.
5. Leftover `/tmp` scratch directories.

`--run-container-ttl` and `--checkout-ttl` stay upper bounds. Each step logs the
free bytes before and after at info level. Low capacity that this pass cannot
fix refuses new work with a resource-specific reason; it does not evict
an active run. Reopening a retained run reuses existing compute and skips
capacity admission.
New browser companions and agent-updater containers share these admission
checks; an updater refusal leaves the installed native agent version available
for the run. Existing environment execs and lookups, including a replay of an
already-created assigned run, do not consume another allowance. Runtime capacity
probes are bounded; remote/unverified Docker hosts or unmeasurable required
storage/memory produce an explicit unavailable refusal rather than guessed capacity. A custom
runtime without the capacity interface keeps the data-filesystem check only.
These checks reduce host pressure but cannot guarantee that all running workloads
fit: a run's memory limit covers its native agent **and all its subprocesses**,
not a separately protected agent brain. See [environments.md](environments.md)
for container isolation and limit behavior.

Swarms run over conflict coordination, so `--conflict-coordination=false` also
turns them off. The dashboard still offers **Swarm**, but `mission.create`
fails with:

```
swarms need conflict coordination; the server was started with --conflict-coordination=false: scheduler: coordination is unavailable
```

Four things happen on the first start and never need attention again:

1. **The SSH host key** is generated into `<data-dir>/ssh/host_ed25519_key`
   (by setup already, when the edge is on). Clients record its fingerprint on
   first link and print it, and the edge derives the server id from it. Do
   not delete it: clients that already trust it refuse to connect until you
   clear the entry from their `known_hosts`, and at the edge a new key is a
   new server that must be claimed and linked again.
2. **The first identity to link becomes the admin** - the SSH key, or the
   tailnet login, of whoever runs `aether link` first, or the edge account
   that uses the claim code. There is no other account creation step.
3. **The SQLite store and the git repo root** are created under the data
   directory.
4. **The push key** is generated into `<data-dir>/push/vapid_key.pem`. It
   signs [notifications](networking.md#notifications), and every device's
   subscription is bound to it.

Options live in `/etc/aether/server.conf`, not in `ExecStart`. The file is
operator-owned, so binary updates and unit reinstalls never rewrite it, and
re-running `aether-server setup` or `install` keeps an existing config and
unit unless you pass `--force`. Change the config with
`aether-server config set <key> <value>` (or `config edit`), then restart.
`aether-server config show` prints every option with the value that would be
used and where it came from; `config path` prints the file's location.

An option removed in a later release does not stop the server: it logs one
warning naming the key and the file, and boots on the remaining settings.
`config show` flags the same keys so you can drop the lines with
`aether-server config edit` when convenient. A key that was never an option
is still an error, because a typo means a setting you believe is in force
never was.

Key-driven agents read the documented API-key environment variable names
from `/etc/aether/aether-server.env`, which the unit loads if it exists. Provide
those values through your deployment's secret manager; do not commit them or
paste them into public configuration examples.

Subscription logins do **not** go there. They live in the member's persistent
home, which `aether terminal` uses after `aether agent add`. See
[harnesses.md](harnesses.md).

## The client-side sync daemon

Optional, on your machine, once per repo. It fetches server-owned run branches
as agents commit and can push your local base branch in **local-only**
workspaces. Reconnect and live-overlay behavior continue as before.

```sh
# Linux; daemon install prints the activation command on other platforms.
aether daemon install --server <server-host>:2222 --repo ~/code/myproject
systemctl --user daemon-reload && systemctl --user enable --now aether-daemon
```

`daemon install` writes a user-level service definition for your platform and
prints the command that activates it: a systemd user unit
(`~/.config/systemd/user/aether-daemon.service`) on Linux, a launchd agent
(`~/Library/LaunchAgents/com.aether.daemon.plist`) on macOS, and a Scheduled
Task XML (`%USERPROFILE%\aether-daemon.xml`, registered with
`schtasks /Create`) on Windows. `aether daemon run --server ... --repo ...`
does the same work in the foreground on any of them. The daemon syncs git
branches only; it does not watch agent configuration directories. Configuration
is uploaded explicitly with `aether config push`
([Upload agent configuration](quickstart.md#upload-agent-configuration)) and
edited in **Files**.

If a service unit was generated by an older release, it may still contain a
removed argument such as `--no-profile-sync` or `--sync-origin`. The daemon
exits with status 2 on a bad argument or a `--server` that is not
`host:port`. Units written by earlier releases keep restarting until you
reinstall them; a reinstalled unit carries `RestartPreventExitStatus=2`, so
after `systemctl --user daemon-reload` systemd leaves it `failed`, and
`systemctl --user status aether-daemon` shows the reason. Reinstall with the command below and activate the service.
`daemon install` also refuses a `--repo` that is not
inside a git work tree:

```sh
aether daemon install --server <server-host>:2222 --repo ~/code/myproject
systemctl --user daemon-reload && systemctl --user enable --now aether-daemon
```

On macOS or Windows, use the activation command printed by `daemon install`
instead of the Linux `systemctl` command.

The daemon no longer forwards a checkout's `origin` into the workspace base.
It does not refresh a source mirror. In a mirrored workspace, its local base
push attempt is rejected because the server-owned base is protected; use
`aether workspace mirror refresh` and explicit candidate adoption instead.
Local-only workspaces retain normal base pushes. There is no recurring
dashboard **Sync from origin** action.

Units installed by a release that accepted `--sync-origin` still contain that
removed flag and fail after upgrade. Reinstall each unit, then activate the
new definition:

```sh
aether daemon install --server <server-host>:2222 --repo ~/code/myproject
systemctl --user daemon-reload && systemctl --user enable --now aether-daemon
```

## Workspace source mirrors

Workspaces are local-only unless an administrator configures a source mirror.
The mirror source is the read-only repository and branch used to refresh the
workspace base; it is deliberately different from checkout `Origin`, which
is where run branches are pushed for review. Configure and inspect it with:

```sh
aether workspace mirror configure --workspace myproject \
  --source https://github.com/acme/project.git --branch main --auth public
aether workspace mirror refresh --workspace myproject
aether workspace mirror status --workspace myproject
```

In **Onboarding → Repository** and repository settings, admitted members can
read source status when the server advertises `workspace.mirror.status`.
Collaborators can push a confirmed local-only base; mirrored or unconfirmed
bases do not offer a push. Administrators can open the **Workspace > Source
control** management flow, prefilled from checkout **Origin** when available.
Skipping configuration leaves the current source settings unchanged.

Use `--auth deploy-key` for a private GitHub HTTPS source; Aether generates a
key and prints only its public half plus
`https://github.com/<owner>/<repo>/settings/keys/new`. Install that key as a
read-only repository deploy key before **Verify**/`refresh`. For generic SSH,
use an `ssh://` source and `--known-hosts-file <file>` containing the verified
host key. Never put a password, token, or private key in a source URL.

Configuration starts pending. Each manual or run launch refresh fetches exactly
the configured source branch. Forward-only changes advance the accepted base;
rewrites and divergence retain a candidate for explicit
`aether workspace mirror adopt --workspace myproject --generation <n> --yes`.
`disable --yes` restores local-only writes but does not revoke a deploy key at
GitHub; remove it there. Refresh failures are recorded as authentication,
offline, missing-source, rewritten, diverged, or error states and a failed
launch creates no run. A CLI launch may explicitly retry once from the
unchanged accepted commit with `--cached-base <sha>`; no stale fallback is
automatic.

## What lives in the data directory

| Path | Contents |
| --- | --- |
| `aether.db` | SQLite: members, workspaces, runs, event log, and profile metadata. |
| `ssh/` | The server's SSH host key. It derives the server id at an edge; a new key is a new server there. |
| `push/` | The key that signs [push notifications](networking.md#notifications). Without it every device has to turn notifications off and on again. |
| `edge/` | Edge enrollment: the pinned edge key, the owner per edge key under `keys/`, the claim code's hash and the connection status ([edge.md](edge.md#files)). |
| `repos/` | One bare git repo per workspace. |
| `mirrors/` | Per-workspace source-mirror metadata and deploy-key material. Private keys are server-side files, not database columns or member homes. |
| `workspace-secrets/` | One `<workspace-id>.json` per workspace that has secret variables: their values in plain text, mode `0600` in a mode-`0700` directory ([security.md](security.md#workspace-secrets)). |
| `checkouts/` | Per-run worktrees. An explicitly closed Standard or Enhanced run and a completed swarm worker keep their exact checkouts for `--run-container-ttl`; other finished-run checkouts are garbage-collected after `--checkout-ttl`. Open interactive runs are not eligible for finished-checkout cleanup. Each run's `<run-id>.diffsnap/` sidecar holds durable recorded diff history, survives checkout cleanup, and is counted in `worktree_bytes`. |
| `transcripts/` | Per-session PTY recordings (asciicast v2), rotated into 16 MiB files without deleting older segments. Enhanced runs also have an append-only ACP item log. Recordings persist until explicit run/session deletion, not until a byte or age threshold. |
| `homes/<member>/` | One persistent environment home per member: installed agents, vendor login state, configuration uploaded with `aether config push` or edited in Files, and - once that member connects GitHub - their gh token in `.config/gh/hosts.yml` and their commit signing key in `.ssh/aether_signing`. |
| `home-caches/<member>/{runs,terminal}/data/` | Reconstructible npm, pip, uv and Go caches, separate from HOME. Runs use `runs`; the member environment and updater use `terminal`. Small server-owned metadata beside `data/` preserves last use, ownership and cleanup retries; it is not mounted into containers. |
| `profiles/` | Content-addressed agent-profile snapshots. |
| `invites/` | Outstanding one-time invite codes. |
| `coord/` | Per-run coordination sockets and read-only run assets. `coord/<run-id>/captures/` holds explicit browser/terminal PNGs and metadata: at most 64 images, 128 MiB total, 8 MiB each. The limit refuses new captures until deletion; owned mount cleanup removes them. Retained closed TUI and completed swarm runs retain this mount until expiry/deletion. |
| `server-update/` | Server update state: `swap.json`, the last update's versions, outcome and replaced binaries, read before the database opens; `check.json`, the release-check schedule; and `aether.db.pre-update`, the database as it was before an update that migrates it, deleted once that update is recorded `applied`. |
| `scheduler/`, `runtime/` | Scheduler state and the staged MCP bridge binary. Private browser lifecycle journals and control sockets are under `scheduler/browser/<run-hash>/`, not mounted into the run or stored in source. Browser profiles are transient companion state, not saved member images. |

Member homes, mirror credentials and workspace secrets are server-owned
state. Back up the database, `homes/`, `profiles/`, `mirrors/`, and
`workspace-secrets/` when recovery matters. Those backups carry credentials:
every member's vendor logins, GitHub tokens, signing keys, mirror deploy
private keys, and workspace secret values. Encrypt them, restrict access, and
do not publish or paste them into issue reports.
([security.md](security.md#github-credentials-and-signing-keys)).

Diff sidecars retain captured trees until explicit run deletion. Checkout
cleanup preserves their objects and historical ranges; it does not delete
source branches or retained evidence. Consecutive unchanged snapshots do not
add duplicate refs. Published intervals remain recoverable across ref or
event-publication failures.
Watch and current-diff staging allow at most 128 MiB of visible input, while
evidence staging keeps its 64 MiB bound. These are per-operation safety
limits, not stored-history retention limits. Previously missing ranges and
skipped captures remain explicit in Changes; changing retention cannot
recover bytes an older server already deleted.

Each member home is mounted as `$HOME` only in that member's environment
terminal and the runs they launch. An account share additionally mounts the
shared agent's login path from it, the whole `~/.omp/agent` for `omp`, into
the recipient's runs, through a Docker volume named `aether-home-<hash>`
([security.md](security.md#account-sharing)).

Storage rules:

- **Back up `aether.db`, `repos/`, `checkouts/*.diffsnap/`, `transcripts/`,
  `homes/`, `profiles/`, `mirrors/`, and `workspace-secrets/`** to recover core
  state, recorded history, installed agents, credentials, profile snapshots,
  source mirrors and workspace secrets. A database restored without
  `workspace-secrets/` keeps each workspace's script and plain variables and
  has none of its secrets.
- **Durable code and retained runtime have separate lifetimes.** Closed compute
  expires automatically after the `1h` default grace; old retained terminal
  deadlines are shortened on recovery from their durable completion time,
  never extended. Exact-process Reopen ends when compute is released. Finished
  checkouts retain the separate `72h` default, and required evidence, valuable
  Git results and failed cleanup remain protected. Source repositories and
  event history are not cache eviction targets.
- **Managed caches are reclaimed automatically.** Startup after recovery,
  hourly maintenance and the disk-pressure pass reclaim only inactive,
  proven-owned pools. Soft targets are 4 GiB per pool, 16 GiB total and 7 days
  since last genuine use/release. Active, retained, finalizing and uncertain
  owners can exceed those targets; these are not live-write quotas. No idle
  process is killed to meet them. `home-caches/` need not be backed up; keep the
  credentials and installed tools in `homes/`. Settings > **Server** reports
  cache ownership, deadlines and cleanup failures. Byte accounting de-duplicates
  hardlinks and is not a guarantee that deletion returns those bytes to disk.
- **`scratch/` is disposable.** It holds each run's and terminal's `/tmp`,
  is deleted with the container, and the hourly sweep removes leftovers.
  Settings > **Server** lists it as **Scratch**. Do not back it up.
- **User history persists until explicit deletion.** Terminal recordings,
  Enhanced conversations and recorded diff history outlive compute, checkout
  cleanup and archiving. Browser rendering and history requests remain bounded.
  Storage can grow; monitor capacity and explicitly delete unwanted runs,
  swarms or workspaces. Provisioning is refused when reserves and startup
  allowances cannot fit, but active writers can still exhaust a finite disk.
  Local clones share Git objects through hardlinks; accounting charges shared
  objects once rather than once per checkout.
  See [failure handling](failure-handling.md).
- **Keep the path short.** Per-run coordination sockets live under
  `coord/<run-id>/coord3.sock`, and unix socket paths have a hard length limit
  (about 100 characters). A very deep data directory makes the server log
  `coordination unavailable for this run`; inbox commands and native hook
  delivery then have no socket. There is no terminal-notice fallback.
  `/var/lib/aether` is nowhere near the limit.

If you run agents you do not trust, put the data directory on a filesystem
mounted `nosuid,nodev` - the reasoning is in [security.md](security.md).

## Uninstalling

Nothing here is automated: there is no uninstall script and no `make uninstall`,
because removing a server means deleting agent logins and git history that no
script should decide to throw away. The order below is the order that avoids
surprises, and it is also the way to get a clean slate for testing a fresh
install end to end.

### Server

```sh
# 0. With the edge on: unenroll, so the edge stops listing the server.
sudo aether-server edge leave

# 1. Stop the service.
sudo systemctl disable --now aether-server
sudo rm -f /etc/systemd/system/aether-server.service
sudo systemctl daemon-reload

# 2. Containers. Stopping the server does NOT remove them.
sudo docker rm -f $(sudo docker ps -aq --filter label=aether.managed=true)
# Volumes for shared agent accounts. Removing one does not delete a home.
sudo docker volume rm $(sudo docker volume ls -q --filter label=aether.managed=true)
# Remove the standard image and saved member images according to your Docker
# image retention policy.

# 3. State, config, binary.
sudo rm -rf /var/lib/aether /etc/aether
sudo rm -f /usr/local/bin/aether-server
sudo rm -rf /tmp/aether-patch-*
```

Step 2 is the one people miss. The scheduler deliberately leaves run containers
alive across a server restart so it can reattach to them, so they outlive the
unit. Every container the server creates carries `aether.managed=true`; the
label filter includes the browser companions as well as run containers.
Filtering only `--filter name=^/aether-run-` misses companions. Use
`docker ps -a`, not `docker ps`: a crashed run can leave an exited container
behind. The `aether-home-<hash>` volumes carry the same label; remove them
after the containers, since Docker refuses to remove a volume a container
still uses. Remove companion images according to the same Docker image retention
policy as standard and saved member images.

The server writes no log files. Its output goes to the journal, so
`sudo journalctl --rotate && sudo journalctl --vacuum-time=1s` is what clears
the history if you want a silent baseline.

`/etc/aether` only exists if you used `aether-server setup`, `install`,
`config set`, or `config edit`, or if you created `aether-server.env` by hand
for API-key agents. No system user or group is ever created, so there is nothing to
`userdel`.

### Client

```sh
rm -rf ~/.config/aether ~/.config/aether-desktop
ssh-keygen -R '[<server-host>]:2222'
rm -f ~/.local/bin/aether       # the client default
# sudo rm -f /usr/local/bin/aether if you installed there instead
# only if you ran `aether gui build`:
rm -rf ~/.local/share/aether/desktop ~/.local/share/applications/aether-desktop.desktop
rm -rf ~/.cache/aether ~/.cache/electron ~/.cache/electron-builder
```

A client install writes one binary, so `aether` is the only name to remove.
A machine that answered `server` has `aether-server` beside it and the server
list above is the one to follow. An install that named its own components
(`--server`, `AETHER_COMPONENTS`) put whatever it was told wherever
`--bin-dir` pointed; `command -v aether` and `command -v aether-server` find
what is actually there.

The `aether` cache directory - `~/.cache/aether` above, and its macOS and
Windows equivalents below - holds the desktop build directory and the private
Node copy a source build of the desktop app downloads on a machine without
Node 22+.

On macOS the desktop state is `~/Library/Application Support/aether-desktop`,
the app is `/Applications/Aether.app` (or `~/Applications/Aether.app` for a
non-administrator account), and the build caches are
`~/Library/Caches/aether`, `~/Library/Caches/electron`, and
`~/Library/Caches/electron-builder`. On Windows the state is
`%APPDATA%\aether-desktop`, the app is `%LOCALAPPDATA%\Programs\Aether Desktop` plus
its Start Menu shortcut, the build caches are `%LOCALAPPDATA%\aether`,
`%LOCALAPPDATA%\electron`, and `%LOCALAPPDATA%\electron-builder`, and the
client binary is wherever you put `aether.exe` on PATH.

The `ssh-keygen -R` line matters more than it looks. A reinstalled server
generates a new host key, so a stale `known_hosts` entry makes the next
`aether link` fail with a host key mismatch that reads like a bug. Clear the
entry for every address you linked through, including a tailnet name and a raw
IP for the same host.

The client never generates an SSH key of its own. It uses your existing
`~/.ssh/id_ed25519` or whatever your ssh-agent holds, so leave your keys
alone.

If you installed the client daemon, remove it before the binary:

```sh
systemctl --user disable --now aether-daemon
rm -f ~/.config/systemd/user/aether-daemon.service
systemctl --user daemon-reload
```

macOS: `launchctl unload -w ~/Library/LaunchAgents/com.aether.daemon.plist`
then delete the plist. Windows: `schtasks /Delete /TN aether-daemon` then
delete `%USERPROFILE%\aether-daemon.xml`.

### Linked repositories

Each repo you linked has an `aether` git remote and one local branch per run
you pulled. Repoint the base branch **before** removing the remote:

```sh
cd ~/code/myproject
git branch --set-upstream-to=origin/main main   # link may have set this to aether
git branch -D $(git branch --list 'aether/run-*')
git remote remove aether
```

`aether link` sets `branch.<base>.remote` to `aether`. Drop the remote without
repointing and a plain `git push` on that branch starts failing for a reason
that is not obvious. Removing the remote cleans up its remote-tracking refs and
per-branch merge config on its own.

Run `aether sync --live` and left it interrupted? Look for `*.aether-conflict` files
next to your originals: those are your local edits, preserved when a sync
paused. Delete them once you have salvaged what you want.

## Releases

Choose a tag whose exact commit has passed the full `CI` workflow on a push to
`main` in this repository. From a trusted `main` checkout, with `gh`
authenticated and `jq` installed, check the local tag, then push it. Alpha
versions use the same procedure, for example `v0.4.0-alpha.1`:

```sh
sh scripts/release-ci-check.sh 3xDevOps/aether "$(git rev-parse 'v0.4.0-alpha.1^{commit}')" &&
git push origin v0.4.0-alpha.1
```

Do not create the GitHub release yourself. Pushing a `v*` tag runs
[`.github/workflows/release.yml`](../.github/workflows/release.yml), which
takes the release through these states:

1. Build and test every asset and image from the tag's commit.
2. Create the release as a draft with generated notes that start at the
   highest published release below the tag. Drafts are invisible to
   `/releases/latest`, `scripts/install.sh` and update checks.
3. Upload every asset to the draft, delete any asset the draft should not
   have, download all of it back through the API, and check it against the
   signed manifest.
4. Check that the tag still names the commit `release-policy` authorized,
   then publish the draft as a normal release without making it Latest. A tag moved since then
   fails the run and leaves the draft unpublished.
5. Download every asset anonymously, as clients do, and check it again.
6. Mark Latest: the highest version among published, non-prerelease
   releases that carry `aether-release.json`, read from a fresh release
   listing. Every run does this after its own publish and lists again after
   each change until the listing agrees, so Latest ends on the highest
   version even when an older tag finishes after a newer one.

The release is published as a normal release, not a GitHub prerelease,
because `scripts/install.sh` and `internal/selfupdate` resolve GitHub's
`/releases/latest` endpoint, which skips prereleases.

`publisher-policy` passes only when both the tag's pusher and whoever
started the run attempt, including a re-run, are repository admins; anyone
else and failed permission lookups skip the downstream jobs. The workflow
runs from the tagged commit, so also add a tag ruleset that lets only admins
create, update or delete `v*` tags. `release-policy` checks
out trusted `refs/heads/main`, validates the tag syntax, resolves the tag's
full commit SHA and requires it to match the pushed commit. It runs the
checker from that trusted checkout, not from the tag's code.

[`scripts/release-ci-check.sh`](../scripts/release-ci-check.sh) accepts only
this repository's `.github/workflows/ci.yml` (`CI`), with event `push`, branch
`main` and that exact full SHA. It selects the newest matching run and reads
its current attempt; both completion and a `success` conclusion are required.
Missing, pending or failed CI, malformed or incomplete API results and lookup
errors fail closed. A PR run, another commit's green run or an older successful
attempt cannot authorize a release. The accepted run URL and SHA appear in the
release job summary. If CI is still running, wait for it to finish successfully
and rerun the release workflow run for the tag.

The release workflow reuses that full main CI result instead of rerunning Go
vet and unit tests. After policy succeeds, every source-building producer
checks out the authorized SHA and runs independently:

- `release-binaries` has six Go lanes: Linux, macOS (`darwin`) and Windows,
  each for amd64 and arm64. Each Linux lane builds server, edge and CLI;
  each macOS or Windows lane builds only its CLI. They call
  `make release-binaries VERSION="$GITHUB_REF_NAME"` with platform overrides
  and upload `binaries-go-<goos>-<goarch>`.
- `desktop-windows` packages the desktop app on native amd64
  (`windows-latest`) and arm64 (`windows-11-arm`) runners with
  [`.github/actions/desktop-windows-zip`](../.github/actions/desktop-windows-zip/action.yml):
  it stamps the tag into `desktop/package.json`, runs `npm ci` and
  `electron-builder --dir --win`, checks the stamp inside `app.asar`, and
  zips the unpacked app at the archive root with sorted entries and fixed
  timestamps. It launches the app without a CLI, requires its
  `aether CLI not found` dialog, and uploads `binaries-desktop-windows-<arch>`.
- `windows-defender` scans both Windows CLIs and both desktop zips
  ([Windows Defender and SmartScreen](#windows-defender-and-smartscreen)).
- `android` separately builds and verifies the signed APK and AAB with
  `make android VERSION="$GITHUB_REF_NAME"`, then uploads `binaries-android`.
  It is the only job that receives the Android signing secrets.
- `browser-image`, `edge-image` and `standard-image` each build on native
  amd64 (`ubuntu-latest`) and arm64 (`ubuntu-24.04-arm`) runners. Each loads,
  smoke-tests and pushes the same local image as `<tag>-<arch>`; standard
  arm64 builds do not use QEMU. Browser runs `make browser-smoke`, edge runs
  `sh scripts/edge-image-smoke.sh "$EDGE_IMAGE"`, and standard runs
  `sh scripts/standard-image-smoke.sh "$STANDARD_IMAGE"` for its toolchain
  and native Git checks.

The manifest jobs join the tested architectures and verify their digests.
Browser publishes `ghcr.io/3xdevops/aether-browser:<tag>`; standard publishes
`ghcr.io/3xdevops/aether-standard` under the release tag, full commit SHA and
`sha-<first-seven-SHA-characters>`. Both require anonymous manifest access and
pulls of the tested architecture digests. Edge's private-package exception is
described below.

Each manifest job then attests the index digest and both platform digests
with `actions/attest`: SLSA build provenance, signed through Sigstore and
pushed to the registry next to the image. It verifies each attestation
with `gh attestation verify oci://<image>@<digest> --repo <owner>/<repo>
--signer-workflow <owner>/<repo>/.github/workflows/release.yml --source-ref
refs/tags/<tag> --deny-self-hosted-runners` before the release job may
start. The per-architecture builds set `provenance: false` and `sbom:
false`, so the pushed images carry no attestation manifests of their own
and each index lists exactly linux/amd64 and linux/arm64.

Manifest digest lookups use `docker buildx imagetools inspect <image>
--format '{{.Manifest.Digest}}'`. Do not pipe the inspection output through
an early-exiting reader such as `awk '... { print; exit }'`: closing Docker's
stdout pipe can fail the release with exit code 255 even when the digest is
correct.

The final `release` job requires all six Go lanes, both desktop zips, the
Defender scan, signed Android and all three verified image manifests. It
downloads artifacts from this workflow run only, never promotes unsigned CI
builds, and requires exactly these fourteen nonempty, regular, non-symlink
files in `dist/`:

```text
aether-server-linux-amd64         aether-server-linux-arm64
aether-edge-linux-amd64           aether-edge-linux-arm64
aether-linux-amd64                aether-linux-arm64
aether-darwin-amd64               aether-darwin-arm64
aether-windows-amd64.exe          aether-windows-arm64.exe
aether-desktop-windows-amd64.zip  aether-desktop-windows-arm64.zip
aether-android.apk                aether-android.aab
```

Missing or unexpected files, including unsigned Android names, stop the upload.
The job writes and verifies `checksums.txt` with SHA-256, then signs the
release manifest ([Verified releases](#verified-releases)) with

```sh
go run ./internal/selfupdate/cmd/release-manifest \
  -version <tag> -commit <full-sha> -store-schema <n> -images images.json \
  -out dist dist/<asset>...
```

Earlier steps, run without the secret, print `<n>` with
`go run ./internal/store/cmd/schema-version` and write `images.json` with

```sh
go run ./internal/edge/cmd/release-edge -tag <tag> -image aether-browser=<digest> \
  -image aether-edge=<digest> -image aether-standard=<digest> > images.json
```

from the three manifest jobs' digests, so the signing process links
neither store. `release-edge` takes the edge store schema, compat floor and
protocol from the tag's code and the rest from
[`packaging/edge/upgrade.json`](#edge-upgrade-record), and refuses a stale
record. The generator refuses an `images.json` that lacks an image or whose
edge digest is not the `aether-edge` digest, and after signing checks that
the manifest carries exactly what it was given. The generator reads the ed25519 seed from the
`AETHER_RELEASE_SIGNING_KEY` secret ([Protecting the signing
key](#protecting-the-signing-key)), records `<n>` as `store_schema`, and
checks its own output against the keys the tag's code trusts, so a seed
that does not match fails here. The `signing-key` job, in the `release`
environment, fails the whole release before any build or push when that
secret is not set. [`scripts/publish-release.sh`](../scripts/publish-release.sh)
then runs steps 2 to 5 above. It compares each asset's SHA-256 and size with
the manifest, requires `checksums.txt` to match, and compares
`checksums.txt`, `aether-release.json` and `aether-release.json.sig`
byte for byte with what it signed. `standard-latest` and `edge-latest` run
after the release job and move their `latest` tags to the verified images
only if `gh api repos/<owner>/<repo>/releases/latest` names this tag when
they run; otherwise they leave `latest` alone and say why in the job summary.
Each copies the published index's exact bytes to `latest` with `crane tag
<image>@<digest> latest` (crane is downloaded at a pinned version and
SHA-256), so `latest` has the signed and attested digest, checks that
`latest` resolves to it, then that GitHub's Latest still names this tag. If a parallel release took Latest
in between, the job fails with `GitHub's Latest moved to <tag>`; re-run the
latest jobs of that release.

Push release tags one at a time: GitHub starts no workflow for any tag when
more than three are pushed at once. Each tag has its own concurrency group,
`release-refs/tags/<tag>`, so releases of different tags run in parallel and
one tag never cancels another's run; a second run for the same tag waits for
the first.

The workflow also publishes the [edge](edge.md#in-a-container) image
`ghcr.io/3xdevops/aether-edge` for linux/amd64 and linux/arm64, built from
[`images/edge/Dockerfile`](../images/edge/Dockerfile) without a build cache
and stamped with the release tag and the same short commit as the release
binaries. Nothing reaches a tag before it is tested:

1. `edge-image` builds each architecture on a native runner, runs
   `scripts/edge-image-smoke.sh` on it, and only then pushes it as
   `<tag>-amd64` or `<tag>-arm64`.
2. `edge-manifest` joins those two under the immutable tags: the release
   tag, the full commit SHA and `sha-<short-sha>`. It checks that every tag
   names one index digest and that the index carries the two tested images,
   resolves the tags at the registry, pulls each architecture by its own
   digest, lists the digests in the job summary, and attests and verifies
   the three digests. The release job waits for it, so a failed edge image
   uploads no assets, and signs its digest into the manifest's `edge`
   section.
3. `edge-latest` moves `latest` to that digest only after the release job
   has published the release. A release that fails earlier leaves `latest`
   on the previous release.
4. `stable` moves only when an admin promotes the release
   ([Promoting an edge release to stable](#promoting-an-edge-release-to-stable)).

A new GHCR package starts private. For **edge only**, the workflow does not fail
on that, by design: the image is published and pullable with credentials, and
only an administrator can change the visibility. After the first release that
publishes `aether-edge`, a repository administrator opens
<https://github.com/orgs/3xDevOps/packages/container/package/aether-edge>,
chooses Package settings, and sets Danger Zone > Change visibility to
Public; if the package page does not show the repository, **Connect
repository** links it. Until then `edge-manifest` ends with the warning
`aether-edge is not public` and the release still completes.

The separate `android` release job builds and signs the [Android app](#android-app),
both APK and app bundle, in pinned containers: the dashboard and the CLI the
APK carries first, then the SDK container. It needs Docker on its runner and
four repository secrets: `ANDROID_KEYSTORE_B64` (the release keystore,
base64-encoded), `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS` and
`ANDROID_KEY_PASSWORD`. Its first step, **after release policy succeeds but
before checkout or signing-toolchain setup**, checks that all four are present
and decodes the keystore into the runner's temp directory, outside checkout.
Cleanup runs even on failure. Missing secrets prevent Android from building
and block the final asset upload, but other authorized producers may already
be running. An unsigned APK is worse than no APK, because nothing can update
over it. A PKCS12 keystore, which is what `keytool` writes, holds one password
for the store and the key, so `ANDROID_KEY_PASSWORD` is the same string as
`ANDROID_KEYSTORE_PASSWORD`; only a keystore made as JKS has two. Generate it
with `-validity 10950`: `keytool` defaults to 90 days, and Google Play needs
an upload key whose certificate is still valid after 22 October 2033, with 25
years or more recommended. Both artifacts have to carry that keystore's own
certificate before the assets are uploaded:
[`scripts/android-verify-signature.sh`](../scripts/android-verify-signature.sh)
reads the expected SHA-256 fingerprint out of the keystore with `keytool` and
compares it with what `apksigner` prints for the APK and what `keytool
-printcert` prints for the bundle. A valid signature is not enough on its own
- an artifact signed by anything else would ship under the release name and
could never install over an existing install. Keep the keystore: losing it
means no published release can ever update an installed app again.

On Google Play the same keystore is the upload key: Play App Signing re-signs
what the store serves with a key of its own, so an install from Play and an
install of the release APK never update each other unless this key is enrolled
as the app signing key. That enrolment is possible only until a release rolls
out to the open testing or production track - internal and closed testing do
not lock it - and the path is **Protected with Play > Play Store distribution
> Go to Play app signing > Change the app signing key**, which asks for a copy
of the key. Managing the key yourself costs the enhancements Google adds to
keys it holds, including quantum-ready hybrid signing, and it makes one
GitHub Actions secret both the upload key and the app signing key, which
Google advises against. The choice and its trade-offs are in
[android/listing/README.md](../android/listing/README.md#signing); make it
before the first open-testing or production rollout.

The APK's versionCode is the only thing Android compares when deciding
whether an APK is an update, and it comes from the tag, through
[`scripts/android-version-code.sh`](../scripts/android-version-code.sh):

```
MAJOR * 10000000 + MINOR * 100000 + PATCH * 1000 + rank
```

where rank orders the pre-releases of a version below its final release -
`alpha.N` is 100 + N, `beta.N` is 300 + N, `rc.N` is 500 + N, a final release
is 999. So `v0.4.0-alpha.6` is 400106, `v0.4.0-rc.1` is 400501, `v0.4.0` is
400999, and `v0.4.1-alpha.1` is 401101. Every release scores above the one
before it without depending on branch history, which a commit count does not:
a hotfix on a shorter history could otherwise score below the release it fixes,
and Android would refuse the correctly signed APK as a downgrade. Release
authorization still requires successful exact-commit main push CI.

So a release tag has to be `vMAJOR.MINOR.PATCH` with an optional
`-alpha.N`, `-beta.N` or `-rc.N`. A signed build refuses anything else before
it builds. The ceilings the script enforces - major 209, minor 99, patch 99,
pre-release number 199 - hold the result under Android's 2100000000 maximum
and keep one rank from reaching the next.

An unsigned build takes the same number when the tree's `git describe` output
is a release tag, and falls back to versionCode 1 when it is not - an untagged
tree, a tree with uncommitted changes, or `VERSION=` set to something the
script cannot parse. Either way it is unsigned, so it can never install over a
release.

versionName is the tag without its leading `v`, because Play prints it in the
store listing and Android prints it in the phone's app info, where `v0.4.0`
reads as part of the number. `v0.4.0-alpha.6` ships as versionName
`0.4.0-alpha.6` and versionCode 400106.

A release that fails before publication leaves at most a draft: no
published release, no moved image `latest` tag, nothing installers or update
checks can see. A failure after publication, in the anonymous check or a
`latest` job, leaves the release published and complete: it was verified
through the API first. Fix the cause, then open the failed run under the repository's **Actions** tab and
choose **Re-run failed jobs**, or run
`gh run rerun <run-id> --failed`. The publisher reuses the draft: it uploads
every asset again with `gh release upload --clobber`, removes strays and
verifies again, without changing the release notes. Producer artifact uploads
also allow replacement on rerun. A rerun after the release is published
requires the published `aether-release.json` to name the same commit and
version as the run's build, fails with `tag <tag> was published from commit
<sha>; this run built <sha>` when it does not, verifies every published asset
against that manifest and sets Latest again. Assets signed by a published
manifest are never replaced, so a bad published release needs a new tag. A published release without
`aether-release.json`, left by the old workflow or created by hand, is
incomplete: the publisher returns it to a draft, which takes it off
`/releases/latest`, then completes it like any other draft.

Enable [immutable releases](https://docs.github.com/en/code-security/supply-chain-security/understanding-your-software-supply-chain/immutable-releases)
in the repository settings. The workflow adds every asset while the release
is a draft, so immutability, which applies from publication on, changes
nothing for it and stops anyone from replacing a published asset or moving
its tag. It does not apply to releases published before it is enabled.

Every version-bearing release producer uses the release tag explicitly: Go
uses `make release-binaries VERSION="$GITHUB_REF_NAME"` (including Windows
resources), Android uses `make android VERSION="$GITHUB_REF_NAME"`, and edge
passes `VERSION=${{ github.ref_name }}` and the authorized checkout's short
`COMMIT` as Docker build arguments. Browser uses the release tag in image
references; standard uses it in image references and its OCI version label,
not a Make `VERSION` override. This avoids letting `git describe` choose
between multiple tags on the same commit.

For local builds, `make release-binaries` retains the dashboard build and the
default ten Go binaries but does not invoke Android or Docker. Command-line
`SERVER_PLATFORMS`, `EDGE_PLATFORMS` and `CLI_PLATFORMS` overrides select targets;
an empty value disables that binary family. Windows resources are generated
only for selected Windows CLI targets. For example, from the intended source
checkout:

```sh
# Linux arm64 server, edge and CLI only.
make release-binaries VERSION=v0.4.0-alpha.1 \
  SERVER_PLATFORMS=linux/arm64 EDGE_PLATFORMS=linux/arm64 CLI_PLATFORMS=linux/arm64

# Windows arm64 CLI only.
make release-binaries VERSION=v0.4.0-alpha.1 \
  SERVER_PLATFORMS= EDGE_PLATFORMS= CLI_PLATFORMS=windows/arm64
```

These targets write to `dist/` without clearing files from previous builds.
The full local `make release VERSION=v0.4.0-alpha.1` is unchanged: by default
it builds all ten Go binaries, then Android APK and AAB with the existing
signing environment (unsigned when none is configured). It still needs
Docker for Android. Without an explicit `VERSION`, local builds continue to
use `git describe`; full history also keeps short commit stamping consistent.

### Edge upgrade record

[`packaging/edge/upgrade.json`](../packaging/edge/upgrade.json) holds what
only a person can decide about upgrading an edge to the next release:

| Field | Meaning |
| --- | --- |
| `min_upgrade_from` | Oldest release that may upgrade straight to this one. Raise it when a migration or protocol change needs an intermediate release |
| `manual_steps` | Every operator action an upgrade needs, kept across releases: `id`, `since` (the first release that needs it), `summary`, `doc_url` (https) and `blocking`. A blocking step holds an automatic update from any release below `since` until the operator does it |
| `store_schema`, `store_compat_floor`, `protocol` | The edge store schema, its compat floor and the protocol range the code has now |
| `files` | SHA-256 of `images/edge/Dockerfile`, `packaging/edge/aether-edge.env.example` and `packaging/systemd/aether-edge.service`, the files that shape what an edge host runs |

The last two rows are not decisions but a tripwire. `go test
./internal/edge/cmd/release-edge` fails, naming each value, when they
disagree with the code or the files, and so does the release:

```
release-edge: packaging/edge/upgrade.json is out of date: store_schema is 2, packaging/edge/upgrade.json records 1; ...
```

`go test ./cmd/aether-edge` also fails when the edge reads an
`AETHER_EDGE_*` variable that `packaging/edge/aether-edge.env.example`
does not list, so a new variable changes that file's hash. A pull request
that adds a migration, a protocol version, an environment variable, or a
change to the image or the unit therefore edits this file. Decide then whether
the upgrade needs a manual step or a higher `min_upgrade_from`, and record
the new values. Never remove a step: an edge several releases behind still
needs it.

### Promoting an edge release to stable

`ghcr.io/3xdevops/aether-edge:stable` is the production channel
([In a container](edge.md#in-a-container)). Nothing moves it but
[`.github/workflows/promote-edge.yml`](../.github/workflows/promote-edge.yml),
run by hand. It is manual because a production edge is one instance with
no fleet to report a bad release, and its database migrations do not run
backwards: a person decides that a release has proved itself.

1. Wait 24 hours after the release is published, and look for problems
   reported against it.
2. Read the release's digest: `docker buildx imagetools inspect
   ghcr.io/3xdevops/aether-edge:<tag>` prints it as `Digest:`.
3. Run the workflow:

   ```sh
   gh workflow run promote-edge.yml -f tag=<tag> -f digest=sha256:<digest>
   ```

   Add `-f soak_override_reason='<why>'` to promote before 24 hours; the
   reason goes into the job summary.

The workflow runs only when both the user who dispatched it and the user
who started the attempt are repository admins. It checks out `main`,
never builds, and refuses unless:

- the release is published, not a draft;
- `aether-edge release-check`, built from `main` with its embedded release
  keys, verifies the release's `aether-release.json` and finds the input
  digest as `edge.image_digest`;
- the release workflow run that built the tag's commit passed its
  `edge-qualify` job, the edge upgrade qualification, or, in a run that
  has no such job, succeeded as a whole;
- the release was published at least 24 hours ago, or
  `soak_override_reason` is set;
- `ghcr.io/3xdevops/aether-edge:<tag>` resolves to the input digest, the
  index and both platform images carry attestations from this
  repository's release workflow for `refs/tags/<tag>`, and all three read
  anonymously;
- the release is not older than the one `stable` carries, by
  `scripts/android-version-code.sh` order of the image's
  `org.opencontainers.image.version` label. The same release is a no-op;
- the release `stable` carries is at or above the new release's
  `min_upgrade_from`, so hosts following `stable` never land on a release
  they cannot upgrade from. Promote an intermediate release first.

Blocking manual steps do not stop a promotion: each host's updater holds
on them until its operator acts.

It then copies the index's exact bytes to `stable` with `crane tag`,
checks that `stable` resolves to the digest anonymously and that its
attestation verifies, and writes the tag, digest, previous `stable`,
actor, qualification evidence and override reason to the job summary.
Last, also when `stable` already named the digest, it attests the
promotion: predicate type
`https://github.com/3xDevOps/aether/edge-promotion/v1`, predicate
`{"channel":"stable","tag":"<tag>","digest":"<digest>"}`, pushed to the
registry, and verifies it as a host does
([Verifying an image](edge.md#verifying-an-image)).

The admin check and the refusal to run from any ref but `main` cover the
workflow as it is on `main`. `workflow_dispatch` runs the file from the
dispatched ref, so anyone who can push a branch can run an edited copy
with a `packages: write` token and move `stable`. Such a run cannot
produce the promotion attestation hosts require: its certificate names
the branch, not `refs/heads/main`. The `edge-promotion` environment
([Protecting the signing key](#protecting-the-signing-key)) keeps it from
even starting.
Promotions share one concurrency group, `promote-edge`; GitHub keeps one
pending run per group, so dispatch the next only after the previous ends.

To roll back a production edge, pin the previous digest on the host and
restore the database backup taken before the upgrade if its schema moved.
Do not move `stable` back: the workflow refuses an older release, and
other hosts following `stable` would downgrade into a schema they cannot
open.

### Protecting the signing key

The `signing-key` and release jobs run in the GitHub environment `release` and
`promote-edge.yml`'s promotion job in `edge-promotion`; a job that names an
environment creates it on its first run. A repository secret is readable by
any workflow on any branch, so a branch that edits a workflow could read
`AETHER_RELEASE_SIGNING_KEY` and sign a manifest every updater trusts. An
admin closes that in **Settings > Environments**:

1. In `release`, add the secret `AETHER_RELEASE_SIGNING_KEY` with the seed,
   then delete the repository secret of that name. Only jobs that name
   `release` can then read it. Until the repository secret is deleted, it
   still works: GitHub resolves an environment secret first and falls back
   to the repository secret of the same name.
2. In `release`, set **Deployment branches and tags** to **Selected
   branches and tags** with the tag rule `v*`. A workflow edited on a
   branch can name the environment, but GitHub refuses to run that job
   outside a `v*` tag, so the key is read only by a release, whose tags
   only admins may push.
3. In `edge-promotion`, allow only the branch `main`. A promotion from an
   edited copy on another branch then never starts.
4. Optionally, add required reviewers to either environment: each
   promotion then waits for one approval, and each release for two, at
   `signing-key` before any build and at the release job before
   publishing.
