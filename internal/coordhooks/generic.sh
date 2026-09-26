#!/bin/sh
# Register only at a root/main-session native context boundary. Do not run this
# in inherited child hooks, a timer, a watcher, or a background loop.
# The bridge returns a trusted pending-mail instruction, never raw peer content.
# This skeleton prints plain context. Use it directly only if the host accepts
# plain stdout as context. Otherwise capture stdout in the native hook callback
# and put it in the host's documented context field using its JSON serializer
# (for example, return { additionalContext: stdout } in JavaScript). Do not
# interpolate shell text into JSON or register plain stdout as a JSON response.
# Empty stdout means no context; stderr and a nonzero exit mean a real failure.
# Reading this notice is not acknowledgement: the agent must read its Aether
# inbox and explicitly acknowledge each message through the existing workflow.
exec /usr/local/bin/aether-internal hook generic context <<'JSON'
{}
JSON
