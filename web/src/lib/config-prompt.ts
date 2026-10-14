/**
 * The prompt the Agent config files section hands to an agent on the
 * member's own computer. The docs print the same text byte for byte, and the
 * Playwright spec imports it, so it lives outside the React module.
 */
export const configPrompt = `Move my coding-agent configuration from this computer to my Aether server, so agents I run there behave the way they do here. The server runs Linux. Use the \`aether\` CLI, which is linked to it.

1. Run \`aether config help\`. If \`config\` is an unknown command or the help does not list \`config cat\`, run \`aether update\`, which prints the path of the binary it updated. If the help still lacks it, a different \`aether\` comes first in PATH; update that one.
2. Run \`aether config ls\`. It lists each agent Aether can hold configuration for, its directory (the same path here and on the server, such as ~/.claude), whether the agent is installed on the server, and what Aether always leaves out. Run \`aether terminal exec 'echo $HOME; uname -sm'\` for the server's home directory and platform.
3. For each listed directory that exists here, look through it and decide what belongs on the server:
   - Upload: settings, instruction files such as CLAUDE.md or AGENTS.md, custom commands, subagents, skills, hooks, and MCP or plugin configuration.
   - Leave behind: logins, tokens, and API keys; session history, transcripts, caches, and logs; anything that only works on this computer, such as hooks or MCP servers that run programs at local absolute paths, or macOS-only tools.
4. Run \`aether config push --dry-run <agent> <path>...\` on what you chose. Each path is a file or directory inside the agent's directory, relative to it; a directory takes everything in it. The dry run uploads nothing and says what a push would do with each file: \`would create\` it, \`would skip\` it because the server's copy is identical, \`would refuse\` it because the server has a different file, or \`would leave out\` a file Aether never uploads. Read the server's copy of every refused file with \`aether config cat <agent> <path>\` and compare it with mine.
5. Show me the plan and wait for my approval. List separately: new files; files that would replace a different server file, with what differs and whether you recommend replacing it; what you leave behind; and the commands you will run on the server afterwards. Give a one-line reason for each. When a file mixes settings that belong on the server with ones that only work here, write an edited copy to a temporary file and show me the change. Do not edit my local files.
6. Upload what I approved with \`aether config push <agent> <path>...\`. A push never replaces a different server file without \`--overwrite\`; pass it only in a push that names exactly the replacements I approved. Upload an edited copy with \`aether config push <agent> <path> --from <temporary file>\`.
7. Finish on the server with \`aether terminal exec '<command>'\`, which runs one command in the Environment where my agents are installed: for example, reinstall a plugin or MCP server whose configuration you uploaded, then check that it loads.
8. Report what was created, replaced, skipped, and left out, with Aether's reason for each file it left out. End with what I still need to do myself and its exact command, such as logging in to an agent, which needs \`aether terminal\`.

Never print, copy, or upload a credential.`
