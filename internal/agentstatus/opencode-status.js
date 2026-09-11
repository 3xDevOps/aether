// Aether's opencode status reporter. opencode loads this file as a plugin
// and calls the event hook for everything on its own event bus; the events
// that say whether the agent is working or waiting are handed to the staged
// server binary, which reports them over the run's coordination socket.
//
// The command below is internal/mcpbridge.BinaryPath, the server binary the
// scheduler mounts into every run container. The mapping itself lives in
// Go (internal/agentstatus.FromOpenCodeEvent); this file decides only which
// events are worth a report at all.
import { spawn } from "node:child_process"

const reporter = "/opt/aether/aether-server"

export const AetherStatus = async () => {
  // The sessions running a turn right now. opencode gives a subagent a
  // session of its own, and that session going idle is not the run's turn
  // ending, so the reporter speaks for the set rather than for one event.
  const busy = new Set()
  // One report at a time: two reporter processes racing could reach the
  // server in the opposite order and leave the run card saying the wrong
  // thing. The chain is never awaited by the handler - a report must not
  // sit between the agent and its next turn.
  let queue = Promise.resolve()
  let warned = false

  const warn = (err) => {
    if (warned) return
    warned = true
    console.error("aether: opencode status reporter:", err && err.message ? err.message : err)
  }

  const post = (...args) => {
    queue = queue.then(
      () =>
        new Promise((resolve) => {
          const child = spawn(reporter, ["report", "opencode", ...args], { stdio: "ignore" })
          child.on("error", (err) => {
            warn(err)
            resolve()
          })
          child.on("close", () => resolve())
        }),
    )
  }

  return {
    event: async ({ event }) => {
      const session = event && event.properties ? event.properties.sessionID : undefined
      switch (event && event.type) {
        case "session.status": {
          // An idle status and session.idle are published together, and the
          // idle half is read below, where the subagent bookkeeping is.
          const status = event.properties && event.properties.status ? event.properties.status.type : ""
          if (status === "idle") return
          if (session) busy.add(session)
          // A nested session starting a turn inside one that is already
          // running is not news; the run is working either way.
          if (status === "busy" && busy.size <= 1) post("--event", "session.status", "--status", status)
          return
        }
        case "session.idle":
          if (session) busy.delete(session)
          // A subagent finished. The run's own turn has not.
          if (busy.size > 0) return
          post("--event", "session.idle")
          return
        case "permission.asked":
        case "permission.replied":
        case "question.asked":
        case "question.replied":
        case "question.rejected":
          post("--event", event.type)
          return
      }
    },
  }
}
