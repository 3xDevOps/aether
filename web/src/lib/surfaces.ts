import {
  Bot,
  Compass,
  FileText,
  FolderGit2,
  FolderTree,
  History,
  LayoutGrid,
  ListTodo,
  MonitorSmartphone,
  Settings,
  ShieldQuestion,
  SlidersHorizontal,
  SquareTerminal,
  Users,
  type LucideIcon,
} from '@/components/icons'
import type { Capability } from '@/store/hooks'

export interface Surface {
  /** The route name `navigate` takes. */
  name: string
  label: string
  Icon: LucideIcon
  /** `nav` rows sit in the sidebar, `admin` rows under its hairline, and
   * `palette` destinations are reached from the command palette only. */
  place: 'nav' | 'admin' | 'palette'
}

export function surfaces(cap: Capability, admin = false): Surface[] {
  const list: Surface[] = [{ name: 'board', label: 'Board', Icon: LayoutGrid, place: 'nav' }]
  if (cap.hasMethod('mission.list'))
    list.push({ name: 'missions', label: 'Swarms', Icon: ListTodo, place: 'nav' })
  if (cap.hasMethod('workspace.timeline'))
    list.push({ name: 'timeline', label: 'Activity', Icon: History, place: 'nav' })
  if (
    cap.hasMethod('files.tree') ||
    (cap.hasMethod('config.roots') && cap.hasMethod('config.tree'))
  )
    list.push({ name: 'files', label: 'Files', Icon: FolderTree, place: 'nav' })
  if (cap.hasWS('terminal'))
    list.push({ name: 'environment', label: 'Environment', Icon: SquareTerminal, place: 'nav' })
  if (cap.hasMethod('agent.list'))
    list.push({ name: 'agents', label: 'Agents', Icon: Bot, place: 'nav' })
  if (cap.hasMethod('template.save'))
    list.push({ name: 'templates', label: 'Templates', Icon: FileText, place: 'nav' })
  if (cap.hasMethod('member.list'))
    list.push({ name: 'members', label: 'Members', Icon: Users, place: admin ? 'admin' : 'palette' })
  list.push({ name: 'settings', label: 'Settings', Icon: Settings, place: 'admin' })
  if (cap.hasMethod('approval.list'))
    list.push({ name: 'approvals', label: 'Approvals', Icon: ShieldQuestion, place: 'palette' })
  if (cap.hasMethod('config.roots') && cap.hasMethod('config.import'))
    list.push({ name: 'configuration', label: 'Agent config files', Icon: SlidersHorizontal, place: 'palette' })
  if (cap.hasMethod('member.device.list'))
    list.push({ name: 'devices', label: 'Devices', Icon: MonitorSmartphone, place: 'palette' })
  if (cap.hasMethod('workspace.list'))
    list.push({ name: 'workspaces', label: 'Manage workspaces', Icon: FolderGit2, place: 'palette' })
  if (cap.hasLocal('link.status') || (cap.hasMethod('member.git') && cap.hasMethod('agent.list')))
    list.push({ name: 'onboarding', label: 'Onboarding', Icon: Compass, place: 'palette' })
  return list
}

const everything: Capability = { hasMethod: () => true, hasLocal: () => true, hasWS: () => true }

/** A page this gateway or member is not offered, such as a stale shared link. */
export function withheld(name: string, cap: Capability): boolean {
  const named = (list: Surface[]) => list.some((surface) => surface.name === name)
  return named(surfaces(everything)) && !named(surfaces(cap))
}
