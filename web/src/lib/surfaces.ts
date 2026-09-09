// Rendered by both the sidebar nav and the palette's "Go to" group.

import {
  Bot,
  Compass,
  FileText,
  FolderGit2,
  FolderTree,
  History,
  Settings,
  ShieldQuestion,
  Users,
  type LucideIcon,
} from 'lucide-react'
import type { Capability } from '@/store/hooks'

export interface Surface {
  /** The route name `navigate` takes. */
  name: string
  label: string
  Icon: LucideIcon
}

/**
 * Each entry is gated on the method or local verb that powers its view, so a
 * gateway that cannot serve one never shows the way in.
 */
export function surfaces(cap: Capability): Surface[] {
  const list: Surface[] = []
  if (cap.hasMethod('approval.list'))
    list.push({ name: 'approvals', label: 'Approvals', Icon: ShieldQuestion })
  if (cap.hasMethod('workspace.timeline'))
    list.push({ name: 'timeline', label: 'Activity', Icon: History })
  if (cap.hasMethod('member.list'))
    list.push({ name: 'members', label: 'Members', Icon: Users })
  if (cap.hasMethod('workspace.add'))
    list.push({ name: 'workspaces', label: 'Manage workspaces', Icon: FolderGit2 })
  if (cap.hasMethod('template.save'))
    list.push({ name: 'templates', label: 'Templates', Icon: FileText })
  if (cap.hasMethod('agent.list'))
    list.push({ name: 'agents', label: 'Agents', Icon: Bot })
  if (cap.hasMethod('files.tree'))
    list.push({ name: 'files', label: 'Files', Icon: FolderTree })
  if (cap.hasLocal('link.status'))
    list.push({ name: 'onboarding', label: 'Onboarding', Icon: Compass })
  if (cap.hasLocal('daemon.status'))
    list.push({ name: 'settings', label: 'Settings', Icon: Settings })
  return list
}
