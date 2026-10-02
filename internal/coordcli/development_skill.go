package coordcli

import (
	"fmt"
	"io"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const generalDiscovery = `No live capabilities are assumed. Run aether-internal status inside a run
for its current authority; aether-internal --help describes syntax only.
Use aether-internal skill again after identity or assignment changes.
Native Git/gh and image-reading tools depend on your own harness/environment;
Aether does not register tools, install agents, or create a task for discovery.
`

const terminalSkill = `Development terminal loop:
Edit the checkout, start an argv command (or the account shell), then observe
and interact with the same terminal_id + incarnation. No viewer is required.
Raw output is bounded process history, including escapes; rendered screen is
the current cell grid with styles, cursor, geometry and revision. Neither is
a screenshot. Follow output cursors and explicit truncation/gap flags. Page
screens with returned row+column offsets and expected_screen_revision; if the
revision changes, restart observation rather than combining different frames.
Acquire surface {"kind":"terminal","id":terminal_id,"incarnation":incarnation}
with your own opaque control_session_id; use the returned controller's exact
control_generation for input, resize and stop. Repaints do not revoke input;
incarnation or controller changes do. On a stale fence, re-observe ownership;
do not blindly retry input or take over a human. Release the exact lease when
you finish interacting. Release/detach does not stop the process. Never send
development input to the primary harness.
Exercise the changed behavior, inspect output/screen and exit status, correct
problems, and verify again. A successful wait RPC is not a successful process:
inspect matched, timed_out and process.exit_code. Unsupported graphics or
protocol errors are observation limits, not proof the app rendered correctly.
`

const browserSkill = `Headless browser loop:
Status is discovery, not launch. First open uses url, a nonempty caller-chosen
control_session_id, control_generation:0, and no session_id. The broker lazily
creates an isolated companion and acquires its new surface before opening the
page; retain result.page and result.control. Once a session exists, missing
session_id is refused: use status/pages and acquire surface
{"kind":"browser","id":"browser","incarnation":session_id} when needed.
Status state distinguishes not_started, creating, running, paused and session_lost.
After a failed initial creation, a pending: session_id is only an opaque recovery
fence, not a page session. Do not retry open to recreate silently. Acquire that
exact browser surface and explicitly reset using its lease to recover; the
broker destroys only its recorded owned companion. Rediscover and acquire the
new session afterwards.
Subsequent mutations carry session_id, the exact controller fence and, for
page operations, page_id + page_revision. Node IDs come from the current
snapshot; coordinate input also carries the observed viewport_id. Re-observe
after navigation, DOM changes or viewport changes; do not replay stale clicks.
Start the app in a development terminal, open its run-local URL, inspect the
DOM/accessibility snapshot, interact, wait for an explicit condition, inspect
console/network failures and capture the rendered page. Correct the checkout
and repeat the same flow. DOM/text output alone is not visual verification.
Reset is explicit and invalidates session/page/control identities; acquire the
new surface before further mutation. Close closes a page. Releasing control or
detaching a viewer does not kill the companion or app.
This runs on a standard headless Ubuntu server: no X11, Wayland, Xvfb, desktop,
host browser or disabled sandbox. The companion shares only the run network;
it does not mount the checkout, member home, credentials or Docker socket.
`

const captureSkill = `Visible evidence:
Screenshot responses contain private server artifact metadata and a read-only
in-run path, never image base64. Open that path with an image-consuming tool
ONLY if your harness actually supports it. If it cannot read images, report
exactly that limitation and the text/DOM checks you did; do not claim visual
verification from text alone. Preserve capture identity/revision, note any
truncation and unsupported content, and do not automatically upload screenshots
publicly. Verification and evidence collection must precede terminal stop,
artifact deletion, or a terminal worker report: success/failure reporting may
clean up all worker development resources. Do not report while a check still
needs them. Read the inbox before the report and take no new work afterwards.
`

const retainCaptureSkill = `Retain only deliberately selected, reviewed captures:
Before artifact deletion, run cleanup, or a success/failure report, call
aether-internal artifact retain --params-file FILE with
{"artifact_ids":["capture-id"],"verification_notes":"What was actually checked and any limits","idempotency_key":"your-unique-key"}.
The selection is 1..64 captures and notes are at most 4096 UTF-8 bytes. The
mounted socket supplies identity; never send run_id. Inspect the returned
packet_id, then use the existing report --evidence-ref <packet_id>. Retain is
not a report and creates no new outcome semantics. A transient path or handle
alone will not survive cleanup. On an uncertain result, inspect evidence and
explicitly reuse the same key and exact request if retrying; do not retry
mutations automatically or create a different key to conceal a failure.
The retained copy is visible under existing evidence permissions and expiry,
which differ from private live-session access. Review pixels, URLs and notes
for sensitive data yourself; no reliable credential redaction is promised.
Retaining never publishes images to a PR. The later packet-retain revision
does not prove the Git boundary of an earlier screenshot; unknown stays unknown.
`

const nativeGitSkill = `Native Git and gh (not an Aether Git RPC/tool registration):
Check command availability in your harness/account environment before use.
Inspect git status --short, git diff, git diff --cached, git branch --show-current
and git remote -v in the run checkout. Review changes and stage exact paths;
respect existing author identity, signing configuration and coauthor trailers.
Do not disable signing or overwrite user configuration to make a commit pass.
Verify behavior and the staged diff before committing; observe the real result.
The workspace mirror's import source is not necessarily the checkout's origin.
Inspect the actual checkout remote before fetch/push. For a fork PR, explicitly
identify the base repository + branch and head owner + branch; inspect gh auth
status and existing PR metadata before create/update to avoid duplicate PRs.
Use native git/gh for authenticated push and PR operations under the run account,
not a second Git engine. Aether capabilities do not prove gh, credentials,
push rights, image readers or a public upload destination exist. Do not silently
change remotes, auto-upload screenshots or merge a PR; follow the assigned
review/approval and mission integration boundaries.
`

func hasSkillCapability(status *protocol.CoordStatusResult, method string) bool {
	if status == nil {
		return false
	}
	for _, capability := range status.Capabilities {
		if capability == method {
			return true
		}
	}
	return false
}

func skillDevelopmentCommands(status *protocol.CoordStatusResult, group string) []string {
	var commands []string
	for _, op := range developmentOperations {
		if (group == "" || op.group == group) && hasSkillCapability(status, op.method) {
			commands = append(commands, "aether-internal "+op.group+" "+op.name)
		}
	}
	return commands
}

func writeDevelopmentEntrypoints(out io.Writer, status *protocol.CoordStatusResult) error {
	var topics []string
	if len(skillDevelopmentCommands(status, "terminal")) > 0 {
		topics = append(topics, "terminal")
	}
	if len(skillDevelopmentCommands(status, "browser")) > 0 {
		topics = append(topics, "browser")
	}
	if hasSkillCapability(status, protocol.MethodDevTerminalStart) {
		topics = append(topics, "git")
	}
	if len(topics) == 0 {
		return nil
	}
	_, err := fmt.Fprintf(out, "Live development topics (load only what you need):\n  aether-internal skill <%s>\nNative Git guidance does not assert installed tools or push authority.\n", strings.Join(topics, "|"))
	return err
}

func writeDevelopmentSkill(out io.Writer, status *protocol.CoordStatusResult, topic string) (int, error) {
	if status == nil {
		if _, err := io.WriteString(out, "No mounted run socket: no live "+topic+" capabilities can be discovered.\n"+generalDiscovery); err != nil {
			return ExitFailure, err
		}
		return ExitOK, nil
	}
	group := topic
	if topic == "git" {
		group = "terminal"
	}
	commands := skillDevelopmentCommands(status, group)
	if len(commands) == 0 || (topic == "git" && !hasSkillCapability(status, protocol.MethodDevTerminalStart)) {
		if _, err := io.WriteString(out, "The live status does not advertise the development capabilities for this topic.\nHelp documents syntax, not authority; native tools are not implied.\n"); err != nil {
			return ExitFailure, err
		}
		return ExitOK, nil
	}
	if _, err := fmt.Fprintf(out, "Run: %s\nAdvertised commands (use each command's --help for typed JSON fields):\n  %s\n", boundedSkillField(status.RunID), strings.Join(commands, "\n  ")); err != nil {
		return ExitFailure, err
	}
	if topic != "git" {
		for _, shared := range []string{"control", "artifact"} {
			if available := skillDevelopmentCommands(status, shared); len(available) > 0 {
				if _, err := fmt.Fprintf(out, "  %s\n", strings.Join(available, "\n  ")); err != nil {
					return ExitFailure, err
				}
			}
		}
	}
	text := nativeGitSkill + "End every pull request description with this line:\n  Opened from Aether run " + boundedSkillField(status.RunID) + "\n"
	switch topic {
	case "terminal":
		text = terminalSkill + captureSkill
	case "browser":
		text = browserSkill + captureSkill
	}
	if topic != "git" && hasSkillCapability(status, protocol.MethodDevArtifactRetain) {
		text += retainCaptureSkill
	}
	if _, err := io.WriteString(out, "Use only operations advertised above; other steps may be unavailable.\n"+text); err != nil {
		return ExitFailure, err
	}
	return ExitOK, nil
}
