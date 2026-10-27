import type { ComponentType } from 'react'
import { Bell, Cable, Eye, Gauge, Globe, Monitor, Network, Rocket, ServerCog, Settings, Sparkles } from '@/components/icons'
import type { Capability } from '@/store/hooks'

export type CategoryID = 'general' | 'launch' | 'connections' | 'notifications' | 'watches' | 'assistant' | 'children' | 'usage' | 'this-computer' | 'server' | 'remote-access'

export type Gates = { cap: Capability; admin: boolean; assistant: boolean; onboarding: boolean; member: boolean; workspace: boolean }

export type Category = {
  id: CategoryID
  group: 'You' | 'This computer' | 'Server'
  title: string
  purpose: string
  Icon: ComponentType
}

export type SettingEntry = {
  id: string
  category: CategoryID
  label: string
  help: string
  keywords?: string
  link?: { name: string; params?: (active: string) => Record<string, string> }
  visible?: (g: Gates) => boolean
}

export const categories: Category[] = [
  { id: 'general', group: 'You', title: 'General', purpose: 'How this dashboard looks and responds, and who you are on this server.', Icon: Settings },
  { id: 'launch', group: 'You', title: 'Launch', purpose: 'The agent, mode, model, account and fallback your launches start from, on every device.', Icon: Rocket },
  { id: 'connections', group: 'You', title: 'Connections', purpose: 'Services Aether works with on your behalf.', Icon: Cable },
  { id: 'notifications', group: 'You', title: 'Notifications', purpose: 'Where Aether reaches you when a run needs you.', Icon: Bell },
  { id: 'watches', group: 'You', title: 'Watches', purpose: 'Runs, swarms and pull requests the server watches for you, and what it told you.', Icon: Eye },
  { id: 'assistant', group: 'You', title: 'Assistant', purpose: 'What the built-in assistant may do without asking.', Icon: Sparkles },
  { id: 'children', group: 'You', title: 'Child runs', purpose: 'Whether agents in your runs may start other agents, and on which accounts.', Icon: Network },
  { id: 'usage', group: 'You', title: 'Usage', purpose: 'Subscription usage of your agent accounts.', Icon: Gauge },
  { id: 'this-computer', group: 'This computer', title: 'This computer', purpose: 'The server this computer links to and the files it syncs.', Icon: Monitor },
  { id: 'server', group: 'Server', title: 'Server', purpose: 'The Aether server every member shares.', Icon: ServerCog },
  { id: 'remote-access', group: 'Server', title: 'Remote access', purpose: 'How this server is reached from outside its network.', Icon: Globe },
]

export const groups = ['You', 'This computer', 'Server'] as const

export const entries: SettingEntry[] = [
  { id: 'theme', category: 'general', label: 'Theme', help: 'System, light or dark.', keywords: 'dark light mode colour color appearance' },
  { id: 'text-size', category: 'general', label: 'Text size', help: 'Interface text. Terminals keep their own zoom.', keywords: 'font zoom larger accessibility appearance' },
  { id: 'keyboard', category: 'general', label: 'Single-key shortcuts', help: 'Keys such as n, ? and g b.', keywords: 'keyboard keys speech hotkeys' },
  { id: 'profile', category: 'general', label: 'Profile', help: 'Name, colour and git identity.', keywords: 'display name git email avatar account sharing', visible: (g) => g.member },
  { id: 'setup-guide', category: 'general', label: 'Setup guide', help: 'Link this computer, add a repository, set up an agent and start a first run.', keywords: 'onboarding wizard', visible: (g) => g.onboarding },
  { id: 'saved-launch-defaults', category: 'launch', label: 'Saved defaults', help: 'The agent, mode, model, account and fallback a launch starts from.', keywords: 'launch defaults agent mode model account fallback remember preselect shared', visible: (g) => g.member && g.cap.hasMethod('launch.defaults.get') },
  { id: 'connectors', category: 'connections', label: 'ChatGPT and Claude', help: 'Connect ChatGPT or Claude to Aether over MCP.', keywords: 'mcp connector chatgpt claude plugin oauth pairing code revoke' },
  { id: 'github', category: 'connections', label: 'GitHub', help: 'Connect GitHub to choose repositories and let runs publish changes.', keywords: 'github oauth repository import', visible: (g) => g.admin && g.cap.hasMethod('github.oauth.status') },
  { id: 'push', category: 'notifications', label: 'Notify this device when a run needs me', help: 'Push notifications on this device.', keywords: 'notification alert phone browser push' },
  { id: 'watch-push', category: 'notifications', label: 'Watch notifications on push devices', help: 'Send watch notices to your push devices, outside quiet hours.', keywords: 'watch notify push phone quiet hours do not disturb', visible: (g) => g.cap.hasMethod('notify.prefs.get') },
  { id: 'quiet-hours', category: 'notifications', label: 'Quiet hours', help: 'Hold watch pushes overnight and send one summary when they end.', keywords: 'quiet hours night sleep do not disturb mute time zone', visible: (g) => g.cap.hasMethod('notify.prefs.get') },
  { id: 'slack', category: 'notifications', label: 'Slack', help: 'Get a direct message in Slack when a run needs you.', keywords: 'slack dm message' },
  { id: 'watch-list', category: 'watches', label: 'Watches', help: 'What the server watches for you, its state and every notice it sent.', keywords: 'watch follow notify subscribe run swarm pull request ci merged review', visible: (g) => g.member && g.cap.hasMethod('watch.list') },
  { id: 'auto-approve', category: 'assistant', label: 'Let the assistant act without asking', help: 'Launch, message and stop your runs without an approval card.', keywords: 'auto approve approval card', visible: (g) => g.assistant },
  { id: 'child-runs', category: 'children', label: 'Let my runs start child runs', help: 'Agents in your runs start other agents, such as a reviewer on another model.', keywords: 'child runs delegate subagent review agent', visible: (g) => g.member && g.cap.hasMethod('child.settings') },
  { id: 'child-fallback', category: 'children', label: 'Allow automatic fallback', help: "Moves a child run off an account at 95% of its quota.", keywords: 'child runs fallback quota account', visible: (g) => g.member && g.cap.hasMethod('child.settings') },
  { id: 'child-shared', category: 'children', label: "Use subscriptions shared with me when I don't have my own", help: "Starts a child run on a teammate's shared account when you're not signed in to the agent.", keywords: 'child runs shared account subscription', visible: (g) => g.member && g.cap.hasMethod('child.settings') },
  { id: 'child-limits', category: 'children', label: 'Child-run limits', help: 'How many child runs a run tree may have busy, deep and in total.', keywords: 'child runs limits depth queue', visible: (g) => g.member && g.workspace && g.cap.hasMethod('child.settings') && g.cap.hasMethod('workspace.child_limits') },
  { id: 'subscription-usage', category: 'usage', label: 'Subscription usage', help: 'Subscription usage the server reads for Claude and Codex logins.', keywords: 'quota limit subscription claude codex', visible: (g) => g.member },
  { id: 'link', category: 'this-computer', label: 'Linked server', help: 'The server and repository this computer links to, and saved servers.', keywords: 'link switch server repository', visible: (g) => g.cap.hasLocal('link.status') },
  { id: 'sync-daemon', category: 'this-computer', label: 'Sync daemon', help: "Fetches each run's branch into your clone as the agent commits.", keywords: 'daemon fetch branch clone', visible: (g) => g.cap.hasLocal('daemon.status') },
  { id: 'mirror', category: 'this-computer', label: 'Mirror run files', help: "Copies a run's files into your linked repository.", keywords: 'sync overlay files editor', visible: (g) => g.cap.hasLocal('sync.status') },
  { id: 'danger-zone', category: 'this-computer', label: 'Clear local state', help: 'Forget every server, edge sign-in and device key on this computer.', keywords: 'reset logout disconnect clear forget danger', visible: (g) => g.cap.hasLocal('link.reset') },
  { id: 'version', category: 'server', label: 'Version', help: 'Server version and updates.', keywords: 'update upgrade release tailnet' },
  { id: 'disk', category: 'server', label: 'Disk and performance', help: 'CPU, memory, disk and Docker storage.', keywords: 'disk storage cpu memory insights system' },
  { id: 'docker', category: 'server', label: 'Docker inside runs', help: 'Lets agents build and run containers inside their runs.', keywords: 'sysbox docker compose buildx container', visible: (g) => g.admin && g.cap.hasMethod('server.sysbox.status') },
  { id: 'retained-containers', category: 'server', label: 'Retained containers', help: 'Remove containers that finished runs keep.', keywords: 'cleanup free disk release', visible: (g) => g.admin && g.cap.hasMethod('run.release') },
  { id: 'workspace-settings', category: 'server', label: 'Workspace settings and budget', help: "The active workspace's settings, on its page.", keywords: 'budget limit workspace repository', link: { name: 'workspace', params: (active) => ({ workspaceId: active }) }, visible: (g) => g.workspace && g.cap.hasMethod('workspace.settings') },
  { id: 'members', category: 'server', label: 'Members and devices', help: 'Invite members and approve devices, on the Members page.', keywords: 'invite member device approve', link: { name: 'members' }, visible: (g) => g.admin && g.cap.hasMethod('member.list') },
  { id: 'edge', category: 'remote-access', label: 'Remote access', help: 'Edge, server id, access policy and owner.', keywords: 'edge owner claim server id policy tunnel', visible: (g) => g.cap.hasMethod('server.edge') },
]

export function visibleEntries(g: Gates): SettingEntry[] {
  return entries.filter((entry) => !entry.visible || entry.visible(g))
}

export function visibleCategories(shown: SettingEntry[]): Category[] {
  return categories.filter((category) => shown.some((entry) => entry.category === category.id && !entry.link))
}

export function categoryOf(id: string, shown: SettingEntry[]): Category | undefined {
  const categoryID = categories.some((category) => category.id === id) ? id : shown.find((entry) => entry.id === id && !entry.link)?.category
  return visibleCategories(shown).find((category) => category.id === categoryID)
}

export function search(query: string, shown: SettingEntry[]): SettingEntry[] {
  const tokens = query.toLowerCase().split(/\s+/).filter(Boolean)
  const titles = new Map(categories.map((category) => [category.id, category.title]))
  const hits = shown.filter((entry) => {
    const text = [entry.label, entry.help, entry.keywords, titles.get(entry.category)].join(' ').toLowerCase()
    return tokens.every((token) => text.includes(token))
  })
  const inLabel = (entry: SettingEntry) => tokens.every((token) => entry.label.toLowerCase().includes(token))
  return [...hits.filter(inLabel), ...hits.filter((entry) => !inLabel(entry))]
}
