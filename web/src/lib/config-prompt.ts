/**
 * The prompt the Agent config files section hands to an agent on the
 * member's own computer. The docs print the same text byte for byte, and the
 * Playwright spec imports it, so it lives outside the React module.
 */
export const configPrompt = `Move my coding-agent configuration from this computer to my Aether server, so agents I run there behave the way they do here. The server runs Linux. Use the \`aether\` CLI, which is linked to it.

1. Run \`aether config ls\`. It lists each agent Aether can hold configuration for, its directory (the same path here and on the server, such as ~/.claude), and the files Aether always leaves out.
2. For each listed directory that exists here, look through it and decide what belongs on the server:
   - Upload: settings, instruction files such as CLAUDE.md or AGENTS.md, custom commands, subagents, skills, hooks, and MCP or plugin configuration.
   - Leave behind: logins, tokens, and API keys; session history, transcripts, caches, and logs; anything that only works on this computer, such as hooks or MCP servers that run programs at local absolute paths, or macOS-only tools.
   Run \`aether config ls <agent>\` to see what the server already has; a push replaces a server file at the same path.
3. Show me what you will upload and what you will leave behind, with a one-line reason for each, and wait for my approval. Point out files that would need an edit to work on the server. Do not edit my local files.
4. Upload the approved files and directories with \`aether config push <agent> <path>...\`. Each path is a file or directory inside the agent's directory, relative to it; a directory uploads everything in it, and \`.\` uploads the whole directory. Run it with --dry-run first to see what Aether itself will leave out.
5. Report what was uploaded, and every file Aether left out with its reason.
6. End with a list of what I still need to do myself, each with its exact command: for example, reinstalling a plugin or MCP server whose configuration you uploaded, logging in to an agent, or fixing a file that needs a server-side path. Those commands run on the server, in \`aether terminal\`.

Never print, copy, or upload a credential.`
