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
| `client` | `aether` alone, into `~/.local/bin`, created if it is missing. No `sudo`, and the files stay yours. | `aether gui build` - packages and installs the desktop app. Nothing has to be installed first: the CLI downloads its own Node.js when the machine has none. |
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

The script ends by naming the next command for the role you picked and linking
the quickstart. Cancelling setup or the desktop build stops the installer
instead, preserving the interrupted command's exit status.

This script covers Linux and macOS. Windows uses the
[PowerShell installer](#windows-install-script) below.

A checksum mismatch aborts the install. The script needs `curl` or `wget`, and
`sha256sum` or `shasum`.

Options, as flags or environment variables:

| Flag | Variable | Effect |
| --- | --- | --- |
| `--version <tag>` | `AETHER_VERSION` | Install a specific release instead of the latest. |
| `--bin-dir <dir>` | `AETHER_BIN_DIR` | Install somewhere else, whichever role is chosen. |
| `--client` | `AETHER_COMPONENTS=client` | CLI only. |
| `--server` | `AETHER_COMPONENTS=server` | Server only. |
| `--role <role>` | `AETHER_ROLE` | Answer the role question up front: `server`, `client`, or `none` to skip it. |
| | `AETHER_REPO` | Pull from a fork. |
| | `AETHER_BASE_URL` | Pull from a mirror of the release assets. |

Passing flags through a pipe needs `sh -s --`:

```sh
curl -fsSL .../install.sh | sh -s -- --client --bin-dir ~/bin
curl -fsSL .../install.sh | sh -s -- --role server
```

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
`gui build`. The result is the desktop app in the Start Menu as **Aether**.
The CLI supplies Node.js when needed; neither Node nor Go needs installing
first.

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
| `-Version <tag>` | `AETHER_VERSION` | Select a release; otherwise resolve the latest published release. |
| `-BinDir <dir>` | `AETHER_BIN_DIR` | Choose the CLI directory, never the desktop app's managed directory. |
| `-Role client\|none` | `AETHER_ROLE` | `client` installs the CLI and desktop; `none` stops after the CLI. The default is `client`, including unattended runs. |
| | `AETHER_REPO` | Download from a GitHub fork. |
| | `AETHER_BASE_URL` | Download from a release-asset mirror. |

Only the current user's `PATH` is updated, without duplicating the install
directory or removing other entries. Expandable entries such as `%SystemRoot%`
keep their registry type. The current PowerShell process also gets the
directory; existing applications keep their old environment. Sign out and
back in if another terminal still cannot find the CLI. An older CLI,
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
binary alone; a locked binary reports the Windows error rather than deleting
it first. A desktop-build failure keeps the installed CLI, returns failure,
and prints the explicit command to retry. Configuration and SSH files are untouched.

The installer does not request administrator access, change execution policy,
disable Defender, add exclusions, or unblock quarantined files. If policy
blocks unsigned scripts, use an approved script-signing process or the
[manual installation](#manual-install), not a Defender bypass. Downloads and
checksums come from the selected repository or mirror; use only one you
trust. Checksum verification is not code signing and does not guarantee that
Defender will accept an unsigned binary; see [Defender and SmartScreen](#windows-defender-and-smartscreen).

## Upgrading

`aether update` replaces the running CLI with the latest release only if it
is newer (or installs `--version <tag>`), verifying it against the release's
`checksums.txt`. On Linux, an `aether-server` beside the CLI is updated too;
restart it with `sudo systemctl restart aether-server`. The command never asks
for privileges: a binary in a directory you cannot write, `/usr/local/bin` on
a stock install, is refused before anything is downloaded. The refusal names
the probe file it could not create (the number varies) and ends with the
command to run:

```
aether: open /usr/local/bin/.aether-update-probe-1234567890: permission denied: /usr/local/bin is not writable by this user; re-run as `sudo aether update`
```

Re-running either installer upgrades the client without changing its data.
On Windows, close Aether first and rerun `install.ps1`; the default also
rebuilds the desktop app. `aether update` still refuses on Windows.
Replacing the release binary manually remains an option below.

The server's default standard image follows the server build, so a server
update brings that release's environment image with it - though not into a
member environment that is already open
([environments.md](environments.md#the-standard-image)).

### Environment image migration

The member environment image change is one-way. Workspaces no longer carry an
image; existing custom-image workspaces use the standard image after upgrade.
The `--neutral-image` server flag and the `env-edits/` data directory are
gone, and the bootstrap image is no longer published. Saved member images
live only in the server's Docker daemon; `aether env reset` removes a saved
image and returns that member to the standard image.

**From the dashboard.** The **Update now** button installs whatever is newest
at the click: the version in the prompt is re-read first, so it names the
release the install is about to write. It runs the same swap from the
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

A machine with no app installed builds nothing and downloads nothing, so a
server box never sees this step. If the app is running when the rebuild
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
rate limit. A build whose version is `dev` never reports an update. Set
`AETHER_NO_UPDATE_CHECK` to any non-empty value to stop every release check on
an air-gapped machine: the CLI's, the `aether gui` startup line, and the
dashboard's update prompt all answer `disabled` without touching the network.

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
the CLI. The prompt then names both binaries and the
`sudo systemctl restart aether-server` that the running server still needs.

Administrators see a second prompt when the **server** is behind the latest
release. An admin updates it from their laptop, no shell on the server box
needed:

```sh
aether server update [--version <tag>] [--when now|idle] [--cancel] [--yes]
aether server update --status
```

`--when now` (the default) downloads both binaries and verifies them
against `checksums.txt` before replacing either, then renames
`aether-server` and the `aether` beside it into place. It restarts by
re-executing the new binary with the same argv and environment, keeping the
same PID: the shipped unit is `Restart=on-failure`, so a clean exit would
not come back. If the re-exec itself fails under systemd, the server falls
back to `systemctl restart aether-server`.

`--when idle` instead records one pending update, applied the first time no
run is working and no terminal is attached. Two kinds of run do not hold
it back: one parked at `needs-attention`, waiting on a person, and one
paused with `aether pause`, whose container is frozen. Neither has anything
running inside it and both survive the restart like any other run. A second
`--when idle` call replaces the pending one, and `--cancel` clears it.

`--yes` skips the confirmation prompt. `--status` prints the running
version, the latest release, whether this server can update itself, any
pending update and what it is still waiting for, and the outcome of the
last attempt. `server update` is admin only; any member can read
`--status`.

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

Every release publishes bare binaries plus `checksums.txt`:

```
aether-server-linux-amd64   aether-server-linux-arm64
aether-edge-linux-amd64     aether-edge-linux-arm64
aether-linux-amd64          aether-linux-arm64
aether-darwin-amd64         aether-darwin-arm64
aether-windows-amd64.exe    aether-windows-arm64.exe
```

`aether-server` and `aether-edge` are Linux-only. The Windows and macOS assets
are the client. `aether-edge` is only for running your own edge
([edge.md](edge.md)); servers and clients do not need it. It also ships as
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
ARM64 release binaries, downloaded from the build artifact without rebuilding.
The gate records SHA-256 hashes and definition/protection evidence, checks
effective real-time/cloud/sample-submission settings, and rejects missing or
changed files, scan errors, or new detections (including remediated ones).
That verdict covers those bytes and scan conditions, not signing, reputation,
the locally built desktop, or a guarantee of warning-free execution on a
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
dashboard in its own frameless window, with desktop notifications, a
needs-attention badge, and `aether://run/<id>` deep links. It is the same SPA
with the same full SSH authority, just without a browser tab to lose. No
release publishes it; the CLI builds it for you, and needs nothing installed
first. The Windows installer builds it by default; on Linux and macOS,
answering `client` to the install script's question does the same.
This is the command by hand. The window opens at 1280 by 840 and stops
at 960 by 600, the smallest size the dashboard's own layout holds.

```sh
aether gui build
```

On Linux and macOS, cancelling the build with SIGINT exits 130; SIGTERM exits
143. Ordinary build failures exit 1 and print the build's original error.

The CLI carries the shell sources, unpacks them into your cache directory
(`~/.cache/aether/desktop-build` on Linux,
`~/Library/Caches/aether/desktop-build` on macOS,
`%LOCALAPPDATA%\aether\desktop-build` on Windows; `--build-dir` overrides), runs
`npm install` and electron-builder there, and installs the result where your
desktop lists applications:

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
never falls back to a system Node older than 22.
The CLI runs electron-builder through Node directly, avoiding `npx` shell
wrappers and their handling of spaces and metacharacters in Windows paths.

The first build downloads the Electron runtime (about 100 MB) into
electron-builder's own cache (`~/.cache/electron` and
`~/.cache/electron-builder` on Linux, `~/Library/Caches/electron` and
`~/Library/Caches/electron-builder` on macOS, `%LOCALAPPDATA%\electron\Cache`
and `%LOCALAPPDATA%\electron-builder` on Windows), so rebuilding is quick.
Run `aether gui build` again to replace an installed app; on macOS it also
removes an older copy from the other Applications folder. The new app is
staged beside the installed one and swapped in with a rename, so an app that
is running while you rebuild it keeps working until you restart it - deleting
its files under it would take the window down. Windows still holds a running
program's files open, so close the Aether window there first. To remove
everything, delete the two paths in the table, the `aether` cache directory
(the build directory and the private Node copy), and those caches.

`aether gui build --json` prints one JSON line per phase on stdout and leaves
the build's own output on stderr, which is how the dashboard follows a rebuild
it started:

```json
{"phase":"unpacking"}
{"phase":"fetching node"}
{"phase":"installing dependencies"}
{"phase":"packaging"}
{"phase":"installing"}
{"phase":"done","path":"/home/you/.local/share/aether/desktop"}
```

A failure ends with `{"phase":"error","error":"..."}` carrying the build's own
message, and the command still exits non-zero.

The app requires the `aether` CLI installed first; it does not bundle the
binary. As a build-time check, `aether gui build` requires an installed CLI
discoverable through `AETHER_BIN`, then `PATH`, then the installer defaults:
`%LOCALAPPDATA%\Programs\Aether` on Windows, `/usr/local/bin` and
`~/.local/bin` on Linux and macOS. The new shell records the path of the CLI
that built it. At launch, `AETHER_BIN` overrides that path; otherwise the
shell starts the recorded executable, not another copy on the desktop
session's `PATH`. If that executable is gone or cannot run, the error names
its exact path: restore it, set `AETHER_BIN` to a working CLI, or rerun that
installed CLI's `gui build` command. Older shells without a recorded path
still search `PATH` and the installer defaults. This CLI selection is
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

Optional: a shell app that opens the server-hosted dashboard full screen on a
phone. Every release carries two builds of it in its assets and in
`checksums.txt`: `aether-android.apk` for installing straight from the release
page, and `aether-android.aab`, the app bundle Google Play takes. It is not on
Google Play yet; the listing material is in `android/listing/`, and the Play
link replaces this sentence the day the listing goes live. What the app stores
and sends is in [privacy.md](privacy.md); the licences it ships under are in
[notices.md](notices.md).

The app holds no logic and no credential. It is a WebView locked to one HTTPS
origin, so identity stays the phone's own tailnet login, resolved by the server
on every request ([networking.md](networking.md#the-dashboard)). Two things
have to be true first: the server has `web-port` set, and the phone is signed
in to the same tailnet. The app does not use the edge in this release: it
works over a tailnet exactly as before, whatever the server's `edge-url`
and `edge-access` say ([edge.md](edge.md#the-dashboard)).

1. Open the release page in the phone's browser and download
   `aether-android.apk`. Check it against its line in `checksums.txt` if you
   want to.
2. Android asks once for permission to install apps from that browser. Allow
   it, then open the downloaded file.
3. The first screen asks for the server name. Type its MagicDNS name, for
   example `my-server.tailnet-name.ts.net`. A pasted
   `https://my-server.tailnet-name.ts.net/` works too, and a port other than
   443 goes on the end: `my-server.tailnet-name.ts.net:8443`. `http://` is
   refused rather than upgraded.
4. The dashboard opens at the board, already identified. There is no sign-in.

To change the address later, long-press the app icon and pick **Server
address**. An address that does not resolve leaves the WebView's own error page
on screen, which names what actually failed, with a **Server address** button
on it. That screen also carries the link to [privacy.md](privacy.md), the
only place the app itself shows one.

`aether://run/<id>` opens the app on that run, the same link the desktop shell
handles.

The back gesture walks the dashboard's history and then sends the app to the
background instead of closing it, so a terminal keeps its scrollback. Links
that leave the dashboard, such as an agent's OAuth page, open in the phone's
browser.

**Updates install over the old version**, because every release is signed with
the same key. An APK built from a checkout is signed with a different key or
not at all, so Android refuses it as an update; uninstall first, which also
drops the stored server name.

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
workspace container.

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
apply to runs.

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
writes the systemd unit and the config file, and prints the command that
starts the service. On a host that already runs tailscaled, and where tailnet
connections are not required to carry a key, it asks one more question -
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
edge, so an upgrade never enrolls an existing server. With the edge on,
setup ends by printing the server id and a claim code for `aether link
--claim <code>`.
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

Neither command starts anything; both print the activation line so an install
never restarts a live server behind your back:

```sh
systemctl daemon-reload && systemctl enable --now aether-server
systemctl status aether-server
journalctl -u aether-server -f
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

It prints one startup line naming what it bound, the dashboard included when
`--web-port` is set:

```
aether-server <version> serving SSH on :2222 and the dashboard on https://my-server.tailnet-name.ts.net/ (data dir /var/lib/aether)
```

Serve options, which are also the config-file keys. For duration options, `0`
uses the default; negative values have the semantics in the table.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--data-dir` | `/var/lib/aether` | Everything the server owns. |
| `--addr` | `:2222` | The SSH listener. This is the port clients must be able to reach. |
| `--web-port` | `0` (off) | Serve the dashboard over HTTPS on this host's tailnet addresses at this port; `443` makes it `https://<magicdns-name>/`. Needs tailscaled, MagicDNS and HTTPS certificates; see [networking.md](networking.md#the-dashboard). |
| `--standard-image` | `ghcr.io/3xdevops/aether-standard:<build-version>` | Standard image used for members who have not saved an environment. |
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
| `--run-container-ttl` | `1h` | Grace for the exact compute environment after a Standard or Enhanced run closes, its agent report finishes it, or a swarm run completes. `0` uses `1h`; a positive duration overrides it; negative means immediate release. Working/waiting agents and services never expire under this policy. Paused containers still consume RAM during grace. |
| `--min-free-disk` | `0` (automatic) | Free-byte reserve on the Aether data and runtime storage filesystems: 5% of each filesystem, at least 5 GiB and at most 20 GiB. A positive integer overrides the reserve in bytes; negative disables disk admission, not memory checks. Provisioning also needs a 1 GiB startup allowance. |
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

Low capacity refuses new work with a resource-specific reason; it does not evict
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

Three things happen on the first start and never need attention again:

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
is imported explicitly through **Agents → Agent config files** in either the local
dashboard (`aether gui`) or the server-hosted dashboard, and edited in **Files**.

If a service unit was generated by an older release, it may still contain the
removed `--no-profile-sync` argument. Reinstall the unit with the current
daemon command so that argument is removed, then reload and activate the
service:

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
| `edge/` | Edge enrollment: the pinned edge key, the owner per edge key under `keys/`, the claim code's hash and the connection status ([edge.md](edge.md#files)). |
| `repos/` | One bare git repo per workspace. |
| `mirrors/` | Per-workspace source-mirror metadata and deploy-key material. Private keys are server-side files, not database columns or member homes. |
| `checkouts/` | Per-run worktrees. A retained Standard or Enhanced run (closed, or finished by its agent's report) and a completed swarm run keep their exact checkouts for `--run-container-ttl`; other finished-run checkouts are garbage-collected after `--checkout-ttl`. Each run's `<run-id>.diffsnap/` sidecar has bounded retained snapshot history, described below, and is counted in `worktree_bytes`. |
| `transcripts/` | Rolling per-session PTY transcripts (asciicast v2): 16 MiB complete-event segments, a 128 MiB retained target and seven-day sealed-segment age limit, preserving the active segment/latest screen. Enhanced runs retain a rolling latest 64 MiB ACP item log; compaction needs temporary copy space. These bounds do not stop live recording or erase native agent state. |
| `homes/<member>/` | One persistent environment home per member: installed agents, vendor login state, browser-imported and Files-edited configuration, and - once that member connects GitHub - their gh token in `.config/gh/hosts.yml` and their commit signing key in `.ssh/aether_signing`. |
| `home-caches/<member>/{runs,terminal}/data/` | Reconstructible npm, pip, uv and Go caches, separate from HOME. Runs use `runs`; the member environment and updater use `terminal`. Small server-owned metadata beside `data/` preserves last use, ownership and cleanup retries; it is not mounted into containers. |
| `profiles/` | Content-addressed agent-profile snapshots. |
| `invites/` | Outstanding one-time invite codes. |
| `coord/` | Per-run coordination sockets and read-only run assets. `coord/<run-id>/captures/` holds explicit browser/terminal PNGs and metadata: at most 64 images, 128 MiB total, 8 MiB each. The limit refuses new captures until deletion; owned mount cleanup removes them. Retained closed TUI and completed swarm runs retain this mount until expiry/deletion. |
| `scheduler/`, `runtime/` | Scheduler state and the staged MCP bridge binary. Private browser lifecycle journals and control sockets are under `scheduler/browser/<run-hash>/`, not mounted into the run or stored in source. Browser profiles are transient companion state, not saved member images. |

Member homes and mirror credentials are server-owned state. Back up the
database, `homes/`, `profiles/`, and `mirrors/` when recovery matters. Those
backups carry credentials: every member's vendor logins, GitHub tokens, signing
keys, and mirror deploy private keys. Encrypt them, restrict access, and do not
publish or paste them into issue reports.
([security.md](security.md#github-credentials-and-signing-keys)).

Diff sidecars target 512 MiB per run, the newest 1,024 changed trees and seven
days of history. The base, latest and trees needed for the valid current
interval remain protected. Consecutive unchanged snapshots do not add
duplicate refs; changed reverts refresh recency.
Only sidecar refs and unreferenced sidecar objects are pruned, never workspace
branches, source files or retained evidence refs. Legacy `last` markers
migrate their published tip even on a range read without a new capture;
older uncatalogued snapshot history can expire. Published interval refs are
atomic, with two bounded in-flight pins protecting delivered endpoints across
ref/event failures and evidence-triggered GC.
Watch and current-diff staging allow at most 128 MiB of visible input, while
evidence staging keeps its stricter 64 MiB bound. These are soft retained
history budgets, not live disk quotas or limits on valuable workspace data.
Expired ranges and skipped intervals are explicit in Changes. Evidence
imports are serialized against pruning and retain independent durable copies.

Each member home is mounted as `$HOME` only in that member's environment
terminal and the runs they launch. An account share additionally mounts the
shared agent's login path from it, the whole `~/.omp/agent` for `omp`, into
the recipient's runs, through a Docker volume named `aether-home-<hash>`
([security.md](security.md#account-sharing)).

Storage rules:

- **Back up `aether.db`, `repos/`, `homes/`, `profiles/`, and `mirrors/` to
  recover core state, installed agents, login state, profile snapshots, and
  configured source mirrors.**
- **Durable code and retained runtime have separate lifetimes.** Closed compute
  expires automatically after the `1h` default grace; old retained terminal
  deadlines are shortened on recovery from their durable completion time,
  never extended. Exact-process Reopen ends when compute is released. Finished
  checkouts retain the separate `72h` default, and required evidence, valuable
  Git results and failed cleanup remain protected. Source repositories and
  event history are not cache eviction targets.
- **Managed caches are reclaimed automatically.** Startup after recovery,
  hourly maintenance and a single retry on disk pressure reclaim only inactive,
  proven-owned pools. Soft targets are 4 GiB per pool, 16 GiB total and 7 days
  since last genuine use/release. Active, retained, finalizing and uncertain
  owners can exceed those targets; these are not live-write quotas. No idle
  process is killed to meet them. `home-caches/` need not be backed up; keep the
  credentials and installed tools in `homes/`. Settings > **Server** reports
  cache ownership, deadlines and cleanup failures. Byte accounting de-duplicates
  hardlinks and is not a guarantee that deletion returns those bytes to disk.
- **Per-run history is bounded, total valuable data is not.** Rolling
  transcripts and snapshot sidecars reclaim older history automatically, but
  retained runs, workspace files, `aether.db` and `repos/` can still grow.
  Provisioning is refused when filesystem reserves and startup allowances
  cannot fit. Local clones share Git objects through hardlinks; accounting
  charges them once to repositories, not again to each checkout.
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
Node copy `aether gui build` downloads on a machine without Node 22+.

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
authenticated and `jq` installed, check an existing local tag before pushing
it and publishing an ordinary, non-draft GitHub release. Alpha versions use
the same procedure, for example `v0.4.0-alpha.1`:

```sh
sh scripts/release-ci-check.sh 3xDevOps/aether "$(git rev-parse 'v0.4.0-alpha.1^{commit}')" &&
git push origin v0.4.0-alpha.1 &&
gh release create v0.4.0-alpha.1 --title v0.4.0-alpha.1 --generate-notes
```

Publish alpha tags as normal releases, not GitHub prereleases, because
`scripts/install.sh` and `internal/selfupdate` resolve GitHub's
`/releases/latest` endpoint. Do not add `--prerelease` or `--draft`.

Publishing the release runs
[`.github/workflows/release.yml`](../.github/workflows/release.yml). Only an
admin publisher passes `publisher-policy`; other publishers and failed
permission lookups skip the downstream jobs. `release-policy` checks out
trusted `refs/heads/main`, validates the tag syntax, resolves the tag's full
commit SHA and requires it to match the release event's SHA. It runs the
checker from that trusted checkout, not from the tag's code.

[`scripts/release-ci-check.sh`](../scripts/release-ci-check.sh) accepts only
this repository's `.github/workflows/ci.yml` (`CI`), with event `push`, branch
`main` and that exact full SHA. It selects the newest matching run and reads
its current attempt; both completion and a `success` conclusion are required.
Missing, pending or failed CI, malformed or incomplete API results and lookup
errors fail closed. A PR run, another commit's green run or an older successful
attempt cannot authorize a release. The accepted run URL and SHA appear in the
release job summary. If CI is still running, wait for it to finish successfully
and rerun the release workflow.

The release workflow reuses that full main CI result instead of rerunning Go
vet and unit tests. After policy succeeds, every source-building producer
checks out the authorized SHA and runs independently:

- `release-binaries` has six Go lanes: Linux, macOS (`darwin`) and Windows,
  each for amd64 and arm64. Each Linux lane builds server, edge and CLI;
  each macOS or Windows lane builds only its CLI. They call
  `make release-binaries VERSION="$GITHUB_REF_NAME"` with platform overrides
  and upload `binaries-go-<goos>-<goarch>`.
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

Manifest digest lookups use `docker buildx imagetools inspect <image>
--format '{{.Manifest.Digest}}'`. Do not pipe the inspection output through
an early-exiting reader such as `awk '... { print; exit }'`: closing Docker's
stdout pipe can fail the release with exit code 255 even when the digest is
correct.

The final `release` job requires all six Go lanes, signed Android and all three
verified image manifests. It downloads artifacts from this workflow run only,
never promotes unsigned CI builds, and requires exactly these twelve nonempty,
regular, non-symlink files in `dist/`:

```text
aether-server-linux-amd64  aether-server-linux-arm64
aether-edge-linux-amd64    aether-edge-linux-arm64
aether-linux-amd64         aether-linux-arm64
aether-darwin-amd64        aether-darwin-arm64
aether-windows-amd64.exe   aether-windows-arm64.exe
aether-android.apk        aether-android.aab
```

Missing or unexpected files, including unsigned Android names, stop the upload.
The job writes and verifies `checksums.txt` with SHA-256, then uploads all twelve
assets plus the checksum file. Only after that succeeds do `standard-latest`
and `edge-latest` move their respective `latest` tags to the verified images;
each checks that `latest` carries the expected architecture digests. Releases
share one workflow-wide concurrency group. The GitHub release itself is
already published when the workflow starts, so a failed run can still leave
`/releases/latest` pointing at a release without assets; image `latest` is not
moved by a failed pre-upload run.

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
   digest, and lists the digests in the job summary. The release job waits
   for it, so a failed edge image uploads no assets.
3. `edge-latest` moves `latest` to that digest only after the release job
   has uploaded the assets. A release that fails earlier leaves `latest` on
   the previous release.

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
both APK and app bundle, in a pinned SDK container. It needs Docker on its runner
and four repository secrets: `ANDROID_KEYSTORE_B64` (the release keystore,
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

If the release workflow fails, rerun it for the published release after fixing
the cause. The publisher uploads missing assets to the existing release and
replaces same-named assets with `gh release upload --clobber`, without changing
its release notes. Producer artifact uploads also allow replacement on rerun.

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
