// OpenCode V1 1.18.32 server plugin, not a V2 Plugin.define entrypoint.
// One intended root per plugin lifetime; reload the plugin to bind a new root.
import { execFile } from 'node:child_process'

const MESSAGE_ID = 'msg_aether_mailbox_context'

function pendingContext() {
  const { promise, resolve, reject } = Promise.withResolvers()
  const child = execFile('/usr/local/bin/aether-internal', ['hook', 'opencode', 'context'], (error, stdout) => {
    if (error) reject(error)
    else resolve(stdout.trim())
  })
  child.stdin.on('error', reject)
  child.stdin.end('{}')
  return promise
}

export default async function ({ client }) {
  const owner = process.env.AETHER_NATIVE_HOOK_OWNER
  if (owner && owner !== String(process.pid)) return {}
  process.env.AETHER_NATIVE_HOOK_OWNER = String(process.pid)
  let rootSessionID

  return {
    async 'chat.message'(input) {
      if (rootSessionID !== undefined) return
      const { data: session } = await client.session.get({ path: { id: input.sessionID }, throwOnError: true })
      if (!session.parentID && rootSessionID === undefined) rootSessionID = input.sessionID
    },
    async 'experimental.chat.messages.transform'(_input, output) {
      // This V1 boundary has no sessionID in input; derive it from its messages.
      const lastUser = output.messages.findLast(message => message.info.role === 'user' && message.info.id !== MESSAGE_ID)
      if (!lastUser || lastUser.info.sessionID !== rootSessionID) return
      const content = await pendingContext()
      const messages = output.messages.filter(message => message.info.id !== MESSAGE_ID)
      if (content) messages.push({
        info: {
          ...lastUser.info,
          id: MESSAGE_ID,
          time: { created: Date.now() },
        },
        parts: [{
          id: 'prt_aether_mailbox_context',
          sessionID: rootSessionID,
          messageID: MESSAGE_ID,
          type: 'text',
          text: content,
          synthetic: true,
        }],
      })
      // V1 consumes this array by reference. Replace its entries, not retained
      // message objects/parts; no hint is saved to the session transcript.
      output.messages.splice(0, output.messages.length, ...messages)
    },
  }
}
