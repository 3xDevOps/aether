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
make build             # dashboard, then the Go server and CLI into dist/
cd web && bun run dev  # development server
```

The production web build is a Next static export in `web/dist`, embedded into
the Go server and CLI through `web/embed.go`. The CLI serves it from
`aether gui`; the server serves it when `--web-port` is set, so upgrading the
server binary is what refreshes the dashboard a phone loads. Running an
installed server or CLI needs no Node.js and no Next server.

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
the banner shows the `sudo aether update` to run instead (see
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
cannot tell - and never edits a profile for you. The desktop app looks in
`~/.local/bin` itself, so it starts either way; a terminal needs the line. If
an older `aether` is still in `/usr/local/bin`, the script names it and prints
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

Desktop setup requires `v0.4.0-alpha.6` or newer. Older releases are refused
before installation because their desktop build can replace the CLI.
`-Role none` permits a CLI-only installation, including an older release:

```powershell
& "$env:TEMP\aether-install.ps1" -Role none
& "$env:TEMP\aether-install.ps1" -Version v0.4.0-alpha.6 -BinDir "$env:LOCALAPPDATA\AetherTools"
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
The desktop also checks the default CLI directory, so a Start Menu process
with an older `PATH` can find a default installation.

Close Aether before rerunning the installer to upgrade. Downloads and
checksum failures leave the installed binary alone; a locked binary reports
the Windows error rather than deleting it first. A desktop-build failure
keeps the installed CLI, returns failure, and prints the command to retry.
Configuration and SSH files are untouched.

The installer does not request administrator access, change execution policy,
disable Defender, add exclusions, or unblock quarantined files. If policy
blocks unsigned scripts, use an approved script-signing process or the
[manual installation](#manual-install), not a Defender bypass. Downloads and
checksums come from the selected repository or mirror; use only one you
trust. Checksum verification is not code signing and does not guarantee that
Defender will accept an unsigned binary; see [Defender and SmartScreen](#windows-defender-and-smartscreen).

## Upgrading

`aether update` replaces the running CLI with the latest release (or
`--version <tag>`), verifying it against the release's `checksums.txt`. On a
Linux host with `aether-server` installed next to the CLI it updates both and
reminds you to `sudo systemctl restart aether-server`. The command never asks
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
at the click: the version in the banner is re-read first, so it names the
release the install is about to write. It runs the same swap from the
`aether gui` process, which runs as you. A CLI in
a directory you own - `~/.local/bin`, a Homebrew prefix - is replaced without
a question on macOS and Linux (Windows has no self-update). A CLI in a
directory this account cannot write, such as `/usr/local/bin`, splits by
platform, and the banner says which case you are in before you click:

- **macOS.** The banner says *macOS will ask for an administrator password:
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
  changes nothing: the banner says *Update cancelled, nothing was changed.*
  and the button comes back. The button is offered only where the dialog
  can install: the gateway must be in a GUI login session (not started over
  SSH), and only root can write the binary's directory or any directory
  above it. Anywhere else the banner shows `sudo aether update` instead;
  the full rule, and why, sits beside the quoted text in local-gateway.md.
- **Linux.** No button. The banner shows `sudo aether update` to run in a
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
dashboard banner all answer `disabled` without touching the network.

A binary built from a checkout reports what `git describe` produced
(`v1.2.3-4-gabc123`, plus `-dirty` for uncommitted changes). The comparison
reads that as the tag it descends from *plus* commits on top, so such a build
is never told to downgrade to that tag, and is still told about a genuinely
newer release. A checkout with no tags in reach reports a bare commit, which
cannot be ordered against anything and never reports an update.

**In the dashboard.** `aether gui` runs the same check in the background and
prints one line to stderr when a newer release exists. The dashboard shows a
dismissible banner naming the new version, with an **Update now** button that
replaces the binary on this machine. It re-checks about every half hour while
the window is on screen, and again whenever you come back to it, so a release
that lands after launch shows up without restarting the app. The restart takes
the gateway's own work with it - attached terminals and any running
`aether sync` session stop, while the runs themselves keep going on the
server. Dismissing silences that version only - the next release shows the
banner again.

The button does the same two steps the command does. It swaps the binaries,
then rebuilds the app when one is installed, and the banner follows along:
*Updating the CLI...*, then *Rebuilding the app (about a minute; the first
time also fetches Node)...*, then *Relaunching*. On macOS with a binary in
a directory this account cannot write, the first step reads *Downloading
v1.3.0, then macOS asks for an administrator password...* and the dialog
(Touch ID or password) opens once the download is verified; cancelling it
ends the update there with nothing changed.
**Update now** stays disabled until it is over. In the desktop app the shell relaunches itself onto the new
build, so the window you end up in is the new one. In a browser tab the
gateway never exits (it is your terminal's process, not the app's): the app is
still rebuilt, and the banner tells you to restart it.

A rebuild that fails does not cost you the CLI update. The gateway records the
build's error, the desktop app comes back on the new CLI in the old shell, and
the "desktop app is out of date" banner then shows that error above the
`aether gui build` to run by hand. A successful build clears it.

On a single-box install the same update replaces the `aether-server` beside
the CLI. The banner then names both binaries and the
`sudo systemctl restart aether-server` that the running server still needs.

Administrators see a second banner when the **server** is behind the latest
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

**In the dashboard.** An admin does the same from the server banner:
**Update now** asks to confirm, naming how many runs are active first, and
**Update when idle** records the pending update and leaves a **Cancel**
button in its place. The banner then follows the phases live - scheduled,
applying, restarting - and disappears once the server reports the new
version. A failure shows the server's own error and the two commands below.
Every phase is in the workspace activity feed as well, and a member who is
not an admin sees a one-line notice in the status bar while an update is
scheduled or applying, so the restart does not look like an outage. See
[dashboard-frontend.md](dashboard-frontend.md#update-prompts).

On the documented unprivileged install (the server binary's directory not
writable by the server process, see [First boot](#first-boot)), `--status`
reports that the server cannot update itself and `server update` refuses.
The dashboard banner offers no buttons there either: it names the same
reason and these commands, with a copy button. Run them on the server host:

```sh
sudo aether update
sudo systemctl restart aether-server
```

**The desktop app is separate.** The dashboard ships inside the CLI, so
updating the CLI updates the dashboard. The Electron shell around it - window
chrome, notifications, `aether://` deep links - is whatever `aether gui build`
last produced, and records which CLI built it. Both `aether update` and the
dashboard's **Update now** rebuild it for you; the banner below is what is
left when that rebuild was skipped (`--no-app`) or failed. It is not tied to a
release being available, because the usual way to get there is to have just
updated.

## Manual install

Every release publishes bare binaries plus `checksums.txt`:

```
aether-server-linux-amd64   aether-server-linux-arm64
aether-linux-amd64          aether-linux-arm64
aether-darwin-amd64         aether-darwin-arm64
aether-windows-amd64.exe    aether-windows-arm64.exe
```

`aether-server` is Linux-only. The Windows and macOS assets are the client.

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

Use `aether-windows-arm64.exe` on an Arm device. Confirm it works with
`aether version` in a fresh terminal. To build the desktop app, run
`aether gui build` with a release containing the directory fix below.
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
release containing the separate **Aether Desktop** install location and
verify it against that release's `checksums.txt`, as above. From that download
directory, restore the CLI and rebuild the app:

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

### Windows Defender and SmartScreen

The client is not code-signed yet, so Windows can stop it in three different
ways. They are separate systems with separate fixes, and the wording on screen
does not always say which one you hit.

| What you see | What it is | What to do |
| --- | --- | --- |
| The browser refuses the download | SmartScreen, in Edge or Chrome | Keep the file, then verify the hash above |
| "Windows protected your PC" | SmartScreen, on first run | **More info**, then **Run anyway** |
| The file disappears, or "virus detected" | Microsoft Defender antivirus | Verify the hash, then report it - below |

The third one is the antivirus, not SmartScreen, and it has no **Run anyway**.
Do not disable Defender or add an exclusion to recover a quarantined binary.
A matching checksum establishes download integrity, not safety; follow the
report process below.

Smart App Control and organizational policies can also reject unsigned
binaries. The installer does not change those policies.

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
binary and `aether gui build` refuses to run if the shell would not find it.
It looks for `aether` in `AETHER_BIN`, then `PATH`, then the installer
defaults: `%LOCALAPPDATA%\Programs\Aether` on Windows, `/usr/local/bin` and
`~/.local/bin` on Linux and macOS. The application menu can have an older or
different `PATH` than your terminal, so a CLI outside those defaults may work
in the terminal and still fail from the menu; `aether gui build` warns when
it finds `aether` that way. If launch fails with "aether CLI not found",
install the CLI into the default directory or set `AETHER_BIN` to its full
path. That lookup is
the launcher's job alone: once the app is running, the dashboard's harness
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

If the window renders an older dashboard than your checkout, an older `aether`
is on your `PATH`; `aether version` prints the commit it was built from.
Building installers (`.dmg`, `.exe`, AppImage) from a checkout and code signing
are in [CONTRIBUTING.md](../CONTRIBUTING.md#desktop-shell).

## Server prerequisites

- **Linux.** Windows and macOS are client platforms.
- **Docker**, running, with the server's user able to reach its socket. Every
  environment terminal and run is a container. Agent installation happens in
  the member's environment terminal.
- **git** on the host. Bare repos, run checkouts, and diffs are real git.
- **`ssh-keygen`** on the host, from the OpenSSH client package
  (`openssh-client` on Debian and Ubuntu). Git uses it to sign the commits
  Aether makes at the end of a run once a member has connected GitHub; see
  [environment-home.md](environment-home.md#connect-github). `aether github
  connect` refuses rather than generating a key it could not sign with.
- Optionally **Tailscale**, which is the recommended way to make the SSH port
  reachable and the recommended identity layer. See
  [networking.md](networking.md).

## Images and containers

An image is a read-only package used to create containers. A container is one
runtime instance of that image. The server opens terminals only inside
containers, never on the host, and never mounts the Docker socket into a
workspace container.

Every container a member receives - agent runs, workspace shells, and the
environment terminal - starts from that member's saved image. When the member
has not saved one, the server uses its standard image, configured with
`--standard-image`. See [environments.md](environments.md) for the standard
image contents, saving, resetting, and missing-image behavior.

The environment terminal is where members install system packages and
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

## First boot

`aether-server setup` walks you through the install: it asks for the listen
address, data directory, and tailnet policy (Enter accepts each default),
writes the systemd unit and the config file, and prints the command that
starts the service. On a host that already runs tailscaled, and where tailnet
connections are not required to carry a key, it asks one more question -
`Dashboard HTTPS port on the tailnet (0 = off)`, defaulting to `443` on a
fresh config - which is how a phone on the tailnet reaches the dashboard
([networking.md](networking.md#the-dashboard)). Answering `server` to the
install script's question runs it for you; this is the same command by hand.

```sh
sudo aether-server setup
```

For an unattended install, `aether-server install` writes the same files from
flags instead of questions - any serve option below is accepted, and options
you leave off keep tracking the binary's defaults across upgrades:

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
give up.

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
| `--tailnet-auto-join` | off | Tailnet identities join approved instead of pending. |
| `--tailnet-require-key` | off | Tailnet connections must also present a registered SSH key; mutually exclusive with `--web-port`, whose browser cannot present a key. |
| `--conflict-coordination` | on | Let overlapping runs message each other; see [coordination.md](coordination.md). |
| `--stall-threshold` | `10m` | Silence after which a run parks needs-attention; see [failure-handling.md](failure-handling.md). |
| `--poll-interval` | `30s` | How often stalls are checked. |
| `--checkout-ttl` | `72h` | How long a finished run's worktree is kept. Negative disables the GC. |
| `--run-container-ttl` | `1h` | How long an explicitly closed TUI run retains its exact container, checkout, row, member account, and coordination surfaces. `0` uses the `1h` default; negative means no retention and immediate cleanup. |
| `--min-free-disk` | `1GiB` | Free bytes below which new runs are refused. Negative disables the floor. |
| `--harness-definitions` | none | Path to a custom harness registry file; see [harnesses.md](harnesses.md). |

Three things happen on the first start and never need attention again:

1. **The SSH host key** is generated into `<data-dir>/ssh/host_ed25519_key`.
   Clients record its fingerprint on first link and print it. Do not delete it:
   clients that already trust it refuse to connect until you clear the entry
   from their `known_hosts`.
2. **The first identity to link becomes the admin** - the SSH key, or the
   tailnet login, of whoever runs `aether link` first. There is no other
   account creation step.
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

Key-driven harnesses read the documented API-key environment variable names
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
is imported explicitly in the local dashboard (`aether gui`) and edited in
**Files**; the server-hosted dashboard has no laptop directory picker.

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

In local dashboard onboarding, after Repository pushes or reconciles the base,
administrators see an optional inline source-mirror entry when capability
`workspace.mirror.status` exists. It opens the same **Workspace > Source
control** flow and is prefilled from checkout **Origin** when available.
Skipping it leaves the workspace's current source settings unchanged. If no
source mirror is configured, it remains local-only; configure it later from
**Workspace > Source control**.

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
| `ssh/` | The server's SSH host key. |
| `repos/` | One bare git repo per workspace. |
| `mirrors/` | Per-workspace source-mirror metadata and deploy-key material. Private keys are server-side files, not database columns or member homes. |
| `checkouts/` | Per-run worktrees. A retained, explicitly closed TUI run keeps its exact checkout for `--run-container-ttl`; other finished-run checkouts are garbage-collected after `--checkout-ttl`. Each run's diff-snapshot objects sit beside its worktree in `<run-id>.diffsnap/` and are reclaimed with it. That store holds one object per distinct version of every file the run writes, so a run that rewrites a large binary repeatedly grows it by that binary's size each time; it is counted in the `worktree_bytes` the disk gauge reports. |
| `transcripts/` | Per-run PTY recordings (asciicast v2). |
| `homes/<member>/` | One persistent environment home per member: installed agents, vendor login state, browser-imported and Files-edited configuration, and - once that member connects GitHub - their gh token in `.config/gh/hosts.yml` and their commit signing key in `.ssh/aether_signing`. |
| `profiles/` | Content-addressed agent-profile snapshots. |
| `invites/` | Outstanding one-time invite codes. |
| `coord/` | Per-run conflict-coordination sockets, recreated each run. |
| `scheduler/`, `runtime/` | Scheduler state and the staged MCP bridge binary. |

Member homes and mirror credentials are server-owned state. Back up the
database, `homes/`, `profiles/`, and `mirrors/` when recovery matters. Those
backups carry credentials: every member's vendor logins, GitHub tokens, signing
keys, and mirror deploy private keys. Encrypt them, restrict access, and do not
publish or paste them into issue reports.
([security.md](security.md#github-credentials-and-signing-keys)).

Each member home is mounted only in that member's environment terminal and
runs using their account, including runs launched through an explicit
account share.

Three consequences worth knowing:

- **Back up `aether.db`, `repos/`, `homes/`, `profiles/`, and `mirrors/` to
  recover core state, installed agents, login state, profile snapshots, and
  configured source mirrors.**
- **Four of these grow without bound**: `checkouts/` (reclaimed by the TTL
  GC), `transcripts/`, `aether.db` (the event log), and `repos/` (every push,
  run branch and reflog entry stays). The dashboard's disk gauge reports those
  four, and new runs are refused below `--min-free-disk`. A checkout is a
  `git clone --local` of its workspace repo, so its object files are hard
  links to the same bytes in `repos/`; the gauge counts them once, under
  `repos/`, and the checkout line is what reclaiming that checkout would give
  back. See [failure-handling.md](failure-handling.md).
- **Keep the path short.** Per-run coordination sockets live under
  `coord/<run-id>/coord2.sock`, and unix socket paths have a hard length limit
  (about 100 characters). A very deep data directory makes the server log
  `coordination unavailable for this run` and fall back to the overlap notice.
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
# 1. Stop the service.
sudo systemctl disable --now aether-server
sudo rm -f /etc/systemd/system/aether-server.service
sudo systemctl daemon-reload

# 2. Containers. Stopping the server does NOT remove them.
sudo docker rm -f $(sudo docker ps -aq --filter label=aether.managed=true)
# Remove the standard image and saved member images according to your Docker
# image retention policy.

# 3. State, config, binary.
sudo rm -rf /var/lib/aether /etc/aether
sudo rm -f /usr/local/bin/aether-server
sudo rm -rf /tmp/aether-patch-*
```

Step 2 is the one people miss. The scheduler deliberately leaves run containers
alive across a server restart so it can reattach to them, so they outlive the
unit. Every container the server creates carries `aether.managed=true` and is
named `aether-run-<id>`, so either the label filter or
`--filter name=^/aether-run-` finds them. Use `docker ps -a`, not `docker ps`:
a crashed run leaves an exited container behind.

The server writes no log files. Its output goes to the journal, so
`sudo journalctl --rotate && sudo journalctl --vacuum-time=1s` is what clears
the history if you want a silent baseline.

`/etc/aether` only exists if you used `aether-server setup`, `install`,
`config set`, or `config edit`, or if you created `aether-server.env` by hand
for API-key harnesses. No system user or group is ever created, so there is nothing to
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

Push the tag, then publish an ordinary, non-draft GitHub release for it. Alpha
versions use the same tag syntax, for example `v0.4.0-alpha.1`:

```sh
git push origin v0.4.0-alpha.1
gh release create v0.4.0-alpha.1 --title v0.4.0-alpha.1 --generate-notes
```

Publish alpha tags as normal releases, not GitHub prereleases, because
`scripts/install.sh` and `internal/selfupdate` resolve GitHub's
`/releases/latest` endpoint. Do not add `--prerelease` or `--draft`.

Publishing the release runs
[`.github/workflows/release.yml`](../.github/workflows/release.yml): it vets,
runs the unit tests, cross-compiles the full matrix with `make release`, writes
`checksums.txt`, and uploads the binaries and standard image. Only an admin
publisher runs this release job on the self-hosted runner labeled `moss`;
other publishers are skipped.

If the release workflow fails after building, rerun it for the published
release. The publisher uploads missing assets to the existing release and
replaces same-named assets without changing its release notes.

The version the binaries report comes from `git describe`, so tags must be
pushed to the repo the workflow checks out, and the checkout uses full history.
