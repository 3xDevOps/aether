// Rendered by both the sidebar nav and the palette's "Go to" group.

import {
  Bot,
  Compass,
  FileText,
  FolderGit2,
  FolderTree,
  History,
  ListTodo,
  MonitorSmartphone,
  Settings,
  ShieldQuestion,
  SlidersHorizontal,
  Users,
  type LucideIcon,
} from 'lucide-react'
import type { Capability } from '@/store/hooks'

export interface Surface {
  /** The route name `navigate` takes. */
  name: string
  label: string
  Icon: LucideIcon
  group: 'Work' | 'Workspace' | 'Admin' | 'Settings'
}

/**
 * API-backed entries retain their gateway capability gates. Settings also hosts
 * client-local preferences, so it is available through every gateway.
 */
export function surfaces(cap: Capability): Surface[] {
  const list: Surface[] = []
  if (cap.hasMethod('mission.list'))
    list.push({ name: 'missions', label: 'Missions', Icon: ListTodo, group: 'Work' })
  if (cap.hasMethod('approval.list'))
    list.push({ name: 'approvals', label: 'Approvals', Icon: ShieldQuestion, group: 'Work' })
  if (cap.hasMethod('workspace.timeline'))
    list.push({ name: 'timeline', label: 'Activity', Icon: History, group: 'Work' })
  if (
    cap.hasMethod('files.tree') ||
    (cap.hasMethod('config.roots') && cap.hasMethod('config.tree'))
  )
    list.push({ name: 'files', label: 'Files', Icon: FolderTree, group: 'Workspace' })
  if (cap.hasMethod('template.save'))
    list.push({ name: 'templates', label: 'Templates', Icon: FileText, group: 'Workspace' })
  if (cap.hasMethod('agent.list'))
    list.push({ name: 'agents', label: 'Agents', Icon: Bot, group: 'Workspace' })
  if (cap.hasMethod('config.roots') && cap.hasMethod('config.import'))
    list.push({ name: 'configuration', label: 'Configuration', Icon: SlidersHorizontal, group: 'Workspace' })
  if (cap.hasMethod('member.list'))
    list.push({ name: 'members', label: 'Members', Icon: Users, group: 'Admin' })
  if (cap.hasMethod('member.device.list'))
    list.push({ name: 'devices', label: 'Devices', Icon: MonitorSmartphone, group: 'Admin' })
  if (cap.hasMethod('workspace.add'))
    list.push({ name: 'workspaces', label: 'Manage workspaces', Icon: FolderGit2, group: 'Admin' })
  if (cap.hasLocal('link.status'))
    list.push({ name: 'onboarding', label: 'Onboarding', Icon: Compass, group: 'Admin' })
  list.push({ name: 'settings', label: 'Settings', Icon: Settings, group: 'Settings' })
  return list
}
