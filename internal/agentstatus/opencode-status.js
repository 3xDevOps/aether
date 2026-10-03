// OpenCode V1 1.18.32 server plugin. Reporting stays on the staged run socket.
import { spawn } from "node:child_process"

const reporter = "/opt/aether/aether-server"
const key = (session, kind, id) => JSON.stringify([session, kind, id])
const OWNER = Symbol.for("aether.opencode.status")

export const AetherStatus = async ({ client, serverUrl, directory }) => {
  globalThis[OWNER]?.dispose()
  const lifetime = new AbortController()
  const failed = new Map()
  const endingSessions = new Set()
  let retryTimer
  let retryQueued = false
  let activeRead
  let reporting
  const busy = new Set()
  const pending = new Map()
  let queue = Promise.resolve()
  let events = Promise.resolve()
  let warned = false
  let executionKnown = false

  globalThis[OWNER] = { dispose }
  process.once("exit", dispose)

  function dispose() {
    lifetime.abort()
    clearTimeout(retryTimer)
    retryTimer = undefined
    failed.clear()
    reporting?.kill()
    process.off("exit", dispose)
  }
  const warn = async (error) => {
    if (warned || lifetime.signal.aborted) return
    warned = true
    const message = error?.message ?? String(error)
    try {
      await client.app.log({ body: { service: "aether", level: "error", message: "status reporter: " + message } })
    } catch {}
  }

  function report() {
    if (lifetime.signal.aborted) return
    const working = busy.size > 0
    const body = JSON.stringify({
      ...(executionKnown ? { state: working ? "working" : "waiting" } : {}),
      ...(executionKnown && !working ? { reason: "agent idle" } : {}),
      input_updates: [{ operation: "replace", requests: [...pending.values()] }],
    })
    // Repeated Working reports refresh liveness even without output or input changes.
    queue = queue.then(() => {
      if (lifetime.signal.aborted) return
      return new Promise(resolve => {
        const child = reporting = spawn(reporter, ["report", "opencode", "--json", body], { stdio: ["ignore", "ignore", "pipe"] })
        child.stderr.setEncoding("utf8")
        child.stderr.on("data", chunk => {
          const message = chunk.split("\n")[0].trim()
          if (message) void warn(message)
        })
        child.on("error", error => { void warn(error); resolve() })
        child.on("close", () => { if (reporting === child) reporting = undefined; resolve() })
      })
    }).catch(warn)
  }

  function open(session, kind, id) {
    if (typeof session !== "string" || !session || typeof id !== "string" || !id) return false
    pending.set(key(session, kind, id), { id, session_id: session, kind })
    return true
  }

  function scheduleRetry() {
    for (const [scope, entry] of failed) {
      entry.requests = entry.requests.filter(([identity, request]) => pending.get(identity) === request)
      if (!entry.requests.length) failed.delete(scope)
    }
    if (!failed.size || lifetime.signal.aborted) {
      clearTimeout(retryTimer)
      retryTimer = undefined
      return
    }
    if (retryTimer || retryQueued) return
    retryTimer = setTimeout(() => {
      retryTimer = undefined
      retryQueued = true
      events = events.then(async () => {
        if (lifetime.signal.aborted) return
        let changed = false
        for (const [scope, entry] of [...failed]) {
          if (failed.get(scope) !== entry) continue
          const size = pending.size
          await reconcileRequests(entry)
          changed ||= pending.size !== size
        }
        if (changed) report()
      }).catch(warn).finally(() => {
        retryQueued = false
        scheduleRetry()
      })
    }, 1000)
    retryTimer.unref()
  }

  async function reconcileRequests(entry) {
    const { session, kind } = entry
    const scope = JSON.stringify([session, kind])
    const requests = entry.requests.filter(([identity, request]) => pending.get(identity) === request)
    if (!requests.length || lifetime.signal.aborted) {
      failed.delete(scope)
      return true
    }
    if (endingSessions.has(session)) return false
    const controller = new AbortController()
    activeRead = { session, controller }
    try {
      // V1's default plugin client predates the list methods. Use the public
      // serverUrl/HTTP API, not the generated SDK's protected _client field.
      if (!serverUrl) throw new Error("OpenCode plugin did not provide serverUrl for request reconciliation")
      const url = new URL(`/${kind}`, serverUrl)
      if (directory) url.searchParams.set("directory", directory)
      const headers = {}
      const password = process.env.OPENCODE_SERVER_PASSWORD
      if (password) {
        const username = process.env.OPENCODE_SERVER_USERNAME || "opencode"
        headers.Authorization = `Basic ${Buffer.from(`${username}:${password}`).toString("base64")}`
      }
      const signal = AbortSignal.any([AbortSignal.timeout(4000), lifetime.signal, controller.signal])
      const response = await fetch(url, { headers, signal })
      if (!response.ok) throw new Error(`GET /${kind}: ${response.status} ${response.statusText}`)
      const live = await response.json()
      if (!Array.isArray(live) || live.some(request => !request ||
        typeof request.sessionID !== "string" || !request.sessionID ||
        typeof request.id !== "string" || !request.id)) {
        throw new Error(`GET /${kind}: expected a request list`)
      }
      if (lifetime.signal.aborted || controller.signal.aborted) return false
      const ids = new Set(live.filter(request => request.sessionID === session).map(request => request.id))
      for (const [identity, request] of requests) if (!ids.has(request.id)) pending.delete(identity)
      failed.delete(scope)
    } catch (error) {
      if (lifetime.signal.aborted || controller.signal.aborted) return false
      failed.set(scope, { session, kind, requests })
      await warn(error)
    } finally {
      activeRead = undefined
    }
    return true
  }

  async function reconcile(session) {
    for (const kind of ["permission", "question"]) {
      const requests = [...pending].filter(([, request]) => request.session_id === session && request.kind === kind)
      if (!await reconcileRequests({ session, kind, requests })) break
    }
  }

  async function handle(event) {
    if (lifetime.signal.aborted) return
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
        endingSessions.delete(deleted)
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
    scheduleRetry()
  }

  return {
    async dispose() { dispose() },
    event: async ({ event }) => {
      if (lifetime.signal.aborted) return
      if (event?.type === "server.instance.disposed" && event.properties?.directory === directory) return dispose()
      if (event?.type === "session.deleted") {
        const session = event.properties?.info?.id
        if (session) endingSessions.add(session)
        if (activeRead && activeRead.session === session) activeRead.controller.abort()
        for (const [scope, entry] of failed) if (entry.session === session) failed.delete(scope)
        scheduleRetry()
      }
      // Reconciliation and lifecycle changes share an order, but never block
      // the host's event callback on a network request or reporter subprocess.
      events = events.then(() => handle(event)).catch(warn)
    },
  }
}
