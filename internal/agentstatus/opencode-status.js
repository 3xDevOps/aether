// Aether's opencode status reporter. opencode loads this file as a plugin
// and calls the event hook for everything on its own event bus; the events
// that say whether the agent is working or waiting are handed to the staged
// server binary, which reports them over the run's coordination socket.
//
// The command below is internal/mcpbridge.BinaryPath, the server binary the
// scheduler mounts into every run container. The mapping itself lives in Go
// (internal/agentstatus.FromOpenCodeEvent).
import { spawn } from "node:child_process"

const reporter = "/opt/aether/aether-server"

export const AetherStatus = async ({ client }) => {
  // The sessions running a turn right now. opencode gives a subagent a
  // session of its own, and that session going idle is not the run's turn
  // ending, so the reporter speaks for the set rather than for one event.
  const busy = new Set()
  // The permission and question prompts nobody has answered yet, by the id
  // opencode gives each one and quotes back as requestID in the answer. A
  // run can have several open at once - one per session - and the member
  // is still needed until the last of them is answered.
  const pending = new Set()
  // One report at a time: two reporter processes racing could reach the
  // server in the opposite order and leave the run card saying the wrong
  // thing. The chain is never awaited by the handler - a report must not
  // sit between the agent and its next turn.
  let queue = Promise.resolve()
  let warned = false

  // opencode's own log is where the failure goes: the TUI owns stdout and
  // stderr for the whole run, so writing there would corrupt the screen
  // instead of telling anyone anything. This is already the failure path,
  // so a log call that fails itself ends here.
  const warn = async (message) => {
    if (warned || !message) return
    warned = true
    try {
      await client.app.log({ body: { service: "aether", level: "error", message: "status reporter: " + message } })
    } catch {}
  }

  const post = (...args) => {
    queue = queue.then(
      () =>
        new Promise((resolve) => {
          // The reporter exits 0 whatever happens - a hook that fails the
          // agent's turn is worse than a run card that is briefly wrong -
          // and says what went wrong on stderr instead. Nothing else here
          // reads that pipe, so the first line of it is the only trace a
          // member has of a reporter that ran but could not reach the
          // server.
          const child = spawn(reporter, ["report", "opencode", ...args], { stdio: ["ignore", "ignore", "pipe"] })
          child.stderr.setEncoding("utf8")
          child.stderr.on("data", (chunk) => warn(chunk.split("\n")[0].trim()))
          child.on("error", (err) => {
            warn(err && err.message ? err.message : String(err))
            resolve()
          })
          child.on("close", () => resolve())
        }),
    )
  }

  return {
    event: async ({ event }) => {
      const props = (event && event.properties) || {}
      const session = props.sessionID
      switch (event && event.type) {
        case "session.status": {
          // busy is the only status that starts a turn. retry is one
          // provider call being tried again inside a turn the session
          // already announced, and a status a newer opencode invents says
          // nothing this knows how to read; either one in the set would be
          // an entry no idle ever removes, and the run would never park.
          const status = props.status ? props.status.type : ""
          if (status !== "busy") return
          // opencode publishes busy several times in one turn - once when
          // the prompt arrives, once when the runner starts, once per step
          // - and a session already running is not news. Neither is a
          // nested session starting a turn inside one that is already
          // running: the run is working either way.
          const started = session ? !busy.has(session) : true
          if (session) busy.add(session)
          if (started && busy.size <= 1) post("--event", "session.status", "--status", status)
          return
        }
        case "session.idle":
          if (session) busy.delete(session)
          // A prompt nobody has answered outlives the turn that asked it,
          // and it is the more precise reason the member is needed, so the
          // run stays parked on that rather than on this.
          if (busy.size > 0 || pending.size > 0) return
          post("--event", "session.idle")
          return
        case "permission.asked":
        case "question.asked":
          pending.add(props.id || session)
          post("--event", event.type)
          return
        case "permission.replied":
        case "question.replied":
        case "question.rejected":
          pending.delete(props.requestID || session)
          // Answering one prompt while another is still open does not give
          // the run back to the agent.
          if (pending.size > 0) return
          post("--event", event.type)
          return
      }
    },
  }
}
