import { createElement, forwardRef } from 'react'
import {
  ArrowLeft as LucideArrowLeft,
  ArrowUp as LucideArrowUp,
  Bot as LucideBot,
  Boxes as LucideBoxes,
  Check as LucideCheck,
  ChevronDown as LucideChevronDown,
  ChevronRight as LucideChevronRight,
  ChevronLeft as LucideChevronLeft,
  ChevronUp as LucideChevronUp,
  Copy as LucideCopy,
  Ellipsis as LucideEllipsis,
  FileText as LucideFileText,
  Folder as LucideFolder,
  FolderGit2 as LucideFolderGit2,
  GitBranch as LucideGitBranch,
  History as LucideHistory,
  Keyboard as LucideKeyboard,
  ListTodo as LucideListTodo,
  LoaderCircle as LucideLoaderCircle,
  Lock as LucideLock,
  MessageSquare as LucideMessageSquare,
  PanelLeft as LucidePanelLeft,
  Paperclip as LucidePaperclip,
  Pause as LucidePause,
  Pi as LucidePi,
  Play as LucidePlay,
  Plus as LucidePlus,
  RefreshCw as LucideRefreshCw,
  Search as LucideSearch,
  Settings as LucideSettings,
  Shield as LucideShield,
  Square as LucideSquare,
  SquarePi as LucideSquarePi,
  SquareTerminal as LucideSquareTerminal,
  Terminal as LucideTerminal,
  TriangleAlert as LucideTriangleAlert,
  Users as LucideUsers,
  Wrench as LucideWrench,
  X as LucideX,
  LayoutGrid as LucideLayoutGrid,
  Compass as LucideCompass,
  FolderTree as LucideFolderTree,
  MonitorSmartphone as LucideMonitorSmartphone,
  ShieldQuestion as LucideShieldQuestion,
  SlidersHorizontal as LucideSlidersHorizontal,
  ChevronsUpDown as LucideChevronsUpDown,
  User as LucideUser,
  Minus as LucideMinus,
  ArrowUpCircle as LucideArrowUpCircle,
  RotateCcw as LucideRotateCcw,
  ClipboardCopy as LucideClipboardCopy,
  ClipboardPaste as LucideClipboardPaste,
  ScanText as LucideScanText,
  ImageUp as LucideImageUp,
  Camera as LucideCamera,
  ScrollText as LucideScrollText,
  PanelRight as LucidePanelRight,
  CircleCheck as LucideCircleCheck,
  CircleX as LucideCircleX,
  ClipboardCheck as LucideClipboardCheck,
  File as LucideFile,
  FolderPlus as LucideFolderPlus,
  ListFilter as LucideListFilter,
  CircleHelp as LucideCircleHelp,
  Reply as LucideReply,
  ArrowRight as LucideArrowRight,
  WrapText as LucideWrapText,
  Pencil as LucidePencil,
  Globe as LucideGlobe,
  Trash2 as LucideTrash2,
  Circle as LucideCircle,
  CircleDot as LucideCircleDot,
  ArrowRightLeft as LucideArrowRightLeft,
  Brain as LucideBrain,
  ListPlus as LucideListPlus,
  CornerUpRight as LucideCornerUpRight,
  ExternalLink as LucideExternalLink,
  Archive as LucideArchive,
  ArchiveRestore as LucideArchiveRestore,
  ArrowDown as LucideArrowDown,
  Cable as LucideCable,
  ChevronsDown as LucideChevronsDown,
  ChevronsUp as LucideChevronsUp,
  CircleAlert as LucideCircleAlert,
  CloudOff as LucideCloudOff,
  CornerDownLeft as LucideCornerDownLeft,
  Download as LucideDownload,
  GitMerge as LucideGitMerge,
  Hourglass as LucideHourglass,
  House as LucideHouse,
  KeyRound as LucideKeyRound,
  List as LucideList,
  LogIn as LucideLogIn,
  MessageCircleQuestion as LucideMessageCircleQuestion,
  MessageSquarePlus as LucideMessageSquarePlus,
  Monitor as LucideMonitor,
  MonitorCog as LucideMonitorCog,
  Moon as LucideMoon,
  Waypoints as LucideWaypoints,
  PackageX as LucidePackageX,
  Rocket as LucideRocket,
  ServerCog as LucideServerCog,
  ServerOff as LucideServerOff,
  ShieldOff as LucideShieldOff,
  Sun as LucideSun,
  Unplug as LucideUnplug,
  UserPlus as LucideUserPlus,
  WifiOff as LucideWifiOff,
  type LucideIcon,
  type LucideProps,
} from 'lucide-react'

export type { LucideIcon }

// The only module that imports lucide-react; design-system.test.ts enforces it.
function icon(Base: LucideIcon): LucideIcon {
  const Icon = forwardRef<SVGSVGElement, LucideProps>((props, ref) =>
    createElement(Base, { strokeWidth: 1.75, ...props, ref }),
  )
  Icon.displayName = Base.displayName
  return Icon
}

const claudeMark =
  'm4.7144 15.9555 4.7174-2.6471.079-.2307-.079-.1275h-.2307l-.7893-.0486-2.6956-.0729-2.3375-.0971-2.2646-.1214-.5707-.1215-.5343-.7042.0546-.3522.4797-.3218.686.0608 1.5179.1032 2.2767.1578 1.6514.0972 2.4468.255h.3886l.0546-.1579-.1336-.0971-.1032-.0972L6.973 9.8356l-2.55-1.6879-1.3356-.9714-.7225-.4918-.3643-.4614-.1578-1.0078.6557-.7225.8803.0607.2246.0607.8925.686 1.9064 1.4754 2.4893 1.8336.3643.3035.1457-.1032.0182-.0728-.164-.2733-1.3539-2.4467-1.445-2.4893-.6435-1.032-.17-.6194c-.0607-.255-.1032-.4674-.1032-.7285L6.287.1335 6.6997 0l.9957.1336.419.3642.6192 1.4147 1.0018 2.2282 1.5543 3.0296.4553.8985.2429.8318.091.255h.1579v-.1457l.1275-1.706.2368-2.0947.2307-2.6957.0789-.7589.3764-.9107.7468-.4918.5828.2793.4797.686-.0668.4433-.2853 1.8517-.5586 2.9021-.3643 1.9429h.2125l.2429-.2429.9835-1.3053 1.6514-2.0643.7286-.8196.85-.9046.5464-.4311h1.0321l.759 1.1293-.34 1.1657-1.0625 1.3478-.8804 1.1414-1.2628 1.7-.7893 1.36.0729.1093.1882-.0183 2.8535-.607 1.5421-.2794 1.8396-.3157.8318.3886.091.3946-.3278.8075-1.967.4857-2.3072.4614-3.4364.8136-.0425.0304.0486.0607 1.5482.1457.6618.0364h1.621l3.0175.2247.7892.522.4736.6376-.079.4857-1.2142.6193-1.6393-.3886-3.825-.9107-1.3113-.3279h-.1822v.1093l1.0929 1.0686 2.0035 1.8092 2.5075 2.3314.1275.5768-.3218.4554-.34-.0486-2.2039-1.6575-.85-.7468-1.9246-1.621h-.1275v.17l.4432.6496 2.3436 3.5214.1214 1.0807-.17.3521-.6071.2125-.6679-.1214-1.3721-1.9246L14.38 17.959l-1.1414-1.9428-.1397.079-.674 7.2552-.3156.3703-.7286.2793-.6071-.4614-.3218-.7468.3218-1.4753.3886-1.9246.3157-1.53.2853-1.9004.17-.6314-.0121-.0425-.1397.0182-1.4328 1.9672-2.1796 2.9446-1.7243 1.8456-.4128.164-.7164-.3704.0667-.6618.4008-.5889 2.386-3.0357 1.4389-1.882.929-1.0868-.0062-.1579h-.0546l-6.3385 4.1164-1.1293.1457-.4857-.4554.0608-.7467.2307-.2429 1.9064-1.3114Z'

export const Claude: LucideIcon = forwardRef<SVGSVGElement, LucideProps>((props, ref) =>
  createElement(
    'svg',
    { viewBox: '0 0 24 24', width: 24, height: 24, fill: 'currentColor', ...props, ref },
    createElement('path', { d: claudeMark }),
  ),
)
Claude.displayName = 'Claude'

export const ArrowLeft = icon(LucideArrowLeft)
export const ArrowUp = icon(LucideArrowUp)
export const Bot = icon(LucideBot)
export const Boxes = icon(LucideBoxes)
export const Check = icon(LucideCheck)
export const ChevronDown = icon(LucideChevronDown)
export const ChevronRight = icon(LucideChevronRight)
export const ChevronUp = icon(LucideChevronUp)
export const Copy = icon(LucideCopy)
export const Ellipsis = icon(LucideEllipsis)
export const FileText = icon(LucideFileText)
export const FolderGit2 = icon(LucideFolderGit2)
export const GitBranch = icon(LucideGitBranch)
export const History = icon(LucideHistory)
export const Keyboard = icon(LucideKeyboard)
export const ListTodo = icon(LucideListTodo)
export const LoaderCircle = icon(LucideLoaderCircle)
export const Lock = icon(LucideLock)
export const MessageSquare = icon(LucideMessageSquare)
export const PanelLeft = icon(LucidePanelLeft)
export const Paperclip = icon(LucidePaperclip)
export const Pause = icon(LucidePause)
export const Pi = icon(LucidePi)
export const Play = icon(LucidePlay)
export const Plus = icon(LucidePlus)
export const RefreshCw = icon(LucideRefreshCw)
export const Search = icon(LucideSearch)
export const Settings = icon(LucideSettings)
export const Shield = icon(LucideShield)
/** Interrupt. */
export const Square = icon(LucideSquare)
export const SquarePi = icon(LucideSquarePi)
export const SquareTerminal = icon(LucideSquareTerminal)
export const Terminal = icon(LucideTerminal)
export const TriangleAlert = icon(LucideTriangleAlert)
export const Users = icon(LucideUsers)
export const Wrench = icon(LucideWrench)
export const X = icon(LucideX)
export const LayoutGrid = icon(LucideLayoutGrid)
export const Compass = icon(LucideCompass)
export const FolderTree = icon(LucideFolderTree)
export const MonitorSmartphone = icon(LucideMonitorSmartphone)
export const ShieldQuestion = icon(LucideShieldQuestion)
export const SlidersHorizontal = icon(LucideSlidersHorizontal)
export const ChevronsUpDown = icon(LucideChevronsUpDown)
export const User = icon(LucideUser)
export const Minus = icon(LucideMinus)
export const ArrowUpCircle = icon(LucideArrowUpCircle)
export const RotateCcw = icon(LucideRotateCcw)
export const ClipboardCopy = icon(LucideClipboardCopy)
export const ClipboardPaste = icon(LucideClipboardPaste)
export const ScanText = icon(LucideScanText)
export const ImageUp = icon(LucideImageUp)
export const Camera = icon(LucideCamera)
export const WrapText = icon(LucideWrapText)
export const ScrollText = icon(LucideScrollText)
export const PanelRight = icon(LucidePanelRight)
export const CircleCheck = icon(LucideCircleCheck)
export const CircleX = icon(LucideCircleX)
export const ClipboardCheck = icon(LucideClipboardCheck)
export const File = icon(LucideFile)
export const Folder = icon(LucideFolder)
export const FolderPlus = icon(LucideFolderPlus)
export const ListFilter = icon(LucideListFilter)
export const CircleHelp = icon(LucideCircleHelp)
export const Reply = icon(LucideReply)
export const ArrowRight = icon(LucideArrowRight)
export const Pencil = icon(LucidePencil)
export const Globe = icon(LucideGlobe)
export const Trash2 = icon(LucideTrash2)
export const Circle = icon(LucideCircle)
export const CircleDot = icon(LucideCircleDot)
export const ArrowRightLeft = icon(LucideArrowRightLeft)
export const Brain = icon(LucideBrain)
export const ListPlus = icon(LucideListPlus)
export const CornerUpRight = icon(LucideCornerUpRight)
export const ExternalLink = icon(LucideExternalLink)
export const ChevronLeft = icon(LucideChevronLeft)
export const Archive = icon(LucideArchive)
export const ArchiveRestore = icon(LucideArchiveRestore)
export const ArrowDown = icon(LucideArrowDown)
export const Cable = icon(LucideCable)
export const ChevronsDown = icon(LucideChevronsDown)
export const ChevronsUp = icon(LucideChevronsUp)
export const CircleAlert = icon(LucideCircleAlert)
export const CloudOff = icon(LucideCloudOff)
export const CornerDownLeft = icon(LucideCornerDownLeft)
export const Download = icon(LucideDownload)
export const GitMerge = icon(LucideGitMerge)
export const Hourglass = icon(LucideHourglass)
export const House = icon(LucideHouse)
export const KeyRound = icon(LucideKeyRound)
export const List = icon(LucideList)
export const LogIn = icon(LucideLogIn)
export const MessageCircleQuestion = icon(LucideMessageCircleQuestion)
export const MessageSquarePlus = icon(LucideMessageSquarePlus)
export const Monitor = icon(LucideMonitor)
export const MonitorCog = icon(LucideMonitorCog)
export const Moon = icon(LucideMoon)
export const Waypoints = icon(LucideWaypoints)
export const PackageX = icon(LucidePackageX)
export const Rocket = icon(LucideRocket)
export const ServerCog = icon(LucideServerCog)
export const ServerOff = icon(LucideServerOff)
export const ShieldOff = icon(LucideShieldOff)
export const Sun = icon(LucideSun)
export const Unplug = icon(LucideUnplug)
export const UserPlus = icon(LucideUserPlus)
export const WifiOff = icon(LucideWifiOff)
