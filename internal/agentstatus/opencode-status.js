// OpenCode V1 1.18.32 server plugin. Reporting stays on the staged run socket.
import { spawn } from "node:child_process"

const reporter = "/opt/aether/aether-server"
const key = (session, kind, id) => JSON.stringify([session, kind, id])

export const AetherStatus = async ({ client, serverUrl, directory }) => {
  const busy = new Set()
  const pending = new Map()
  let queue = Promise.resolve()
  let events = Promise.resolve()
  let warned = false
  let last
  let executionKnown = false

  const warn = async (error) => {
    if (warned) return
    warned = true
    const message = error?.message ?? String(error)
    try {
      await client.app.log({ body: { service: "aether", level: "error", message: "status reporter: " + message } })
    } catch {}
  }

  function report() {
    const working = busy.size > 0
    const body = JSON.stringify({
      ...(executionKnown ? { state: working ? "working" : "waiting" } : {}),
      ...(executionKnown && !working ? { reason: "agent idle" } : {}),
      input_updates: [{ operation: "replace", requests: [...pending.values()] }],
    })
    if (body === last) return
    last = body
    queue = queue.then(() => new Promise(resolve => {
      const child = spawn(reporter, ["report", "opencode", "--json", body], { stdio: ["ignore", "ignore", "pipe"] })
      child.stderr.setEncoding("utf8")
      child.stderr.on("data", chunk => {
        const message = chunk.split("\n")[0].trim()
        if (message) void warn(message)
      })
      child.on("error", error => { void warn(error); resolve() })
      child.on("close", resolve)
    })).catch(warn)
  }

  function open(session, kind, id) {
    if (typeof session !== "string" || !session || typeof id !== "string" || !id) return false
    pending.set(key(session, kind, id), { id, session_id: session, kind })
    return true
  }

  async function reconcile(session) {
    // V1's default plugin client predates the list methods. Use the public
    // serverUrl/HTTP API, not the generated SDK's protected _client field.
    for (const kind of ["permission", "question"]) {
      const requests = [...pending].filter(([, request]) => request.session_id === session && request.kind === kind)
      if (!requests.length) continue
      try {
        if (!serverUrl) throw new Error("OpenCode plugin did not provide serverUrl for request reconciliation")
        const url = new URL(`/${kind}`, serverUrl)
        if (directory) url.searchParams.set("directory", directory)
        const headers = {}
        const password = process.env.OPENCODE_SERVER_PASSWORD
        if (password) {
          const username = process.env.OPENCODE_SERVER_USERNAME || "opencode"
          headers.Authorization = `Basic ${Buffer.from(`${username}:${password}`).toString("base64")}`
        }
        const response = await fetch(url, { headers, signal: AbortSignal.timeout(4000) })
        if (!response.ok) throw new Error(`GET /${kind}: ${response.status} ${response.statusText}`)
        const live = await response.json()
        if (!Array.isArray(live)) throw new Error(`GET /${kind}: expected a request list`)
        const ids = new Set(live.filter(request => request.sessionID === session).map(request => request.id))
        for (const [identity, request] of requests) if (!ids.has(request.id)) pending.delete(identity)
      } catch (error) { await warn(error) }
    }
  }

  async function handle(event) {
    const props = event?.properties ?? {}
    const session = props.sessionID
    switch (event?.type) {
      case "session.status":
        if (props.status?.type !== "busy" || !session) return
        executionKnown = true
        busy.add(session)
        break
      case "session.idle":
        if (!session) return
        executionKnown = true
        busy.delete(session)
        await reconcile(session)
        break
      case "session.deleted": {
        const deleted = props.info?.id
        if (!deleted) return
        busy.delete(deleted)
        for (const [identity, request] of pending) if (request.session_id === deleted) pending.delete(identity)
        break
      }
      case "permission.asked":
        if (!open(session, "permission", props.id)) return
        break
      case "question.asked":
        if (!open(session, "question", props.id)) return
        break
      case "permission.replied":
        if (!pending.delete(key(session, "permission", props.requestID))) return
        // Reject can resolve other permissions for this session too.
        await reconcile(session)
        break
      case "question.replied":
      case "question.rejected":
        if (!pending.delete(key(session, "question", props.requestID))) return
        break
      default:
        return
    }
    report()
  }

  return {
    event: async ({ event }) => {
      // Reconciliation and lifecycle changes share an order, but never block
      // the host's event callback on a network request or reporter subprocess.
      events = events.then(() => handle(event)).catch(warn)
    },
  }
}
