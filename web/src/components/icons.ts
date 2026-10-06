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
  Eye as LucideEye,
  FileText as LucideFileText,
  Folder as LucideFolder,
  FolderGit2 as LucideFolderGit2,
  GitBranch as LucideGitBranch,
  History as LucideHistory,
  Image as LucideImage,
  Info as LucideInfo,
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
  Send as LucideSend,
  Settings as LucideSettings,
  Shield as LucideShield,
  Sparkles as LucideSparkles,
  Square as LucideSquare,
  SquarePi as LucideSquarePi,
  SquareTerminal as LucideSquareTerminal,
  Terminal as LucideTerminal,
  TriangleAlert as LucideTriangleAlert,
  Users as LucideUsers,
  Wrench as LucideWrench,
  X as LucideX,
  Zap as LucideZap,
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
export const Eye = icon(LucideEye)
export const FileText = icon(LucideFileText)
export const FolderGit2 = icon(LucideFolderGit2)
export const GitBranch = icon(LucideGitBranch)
export const History = icon(LucideHistory)
export const Image = icon(LucideImage)
export const Info = icon(LucideInfo)
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
export const Send = icon(LucideSend)
export const Settings = icon(LucideSettings)
export const Shield = icon(LucideShield)
export const Sparkles = icon(LucideSparkles)
/** Interrupt. */
export const Square = icon(LucideSquare)
export const SquarePi = icon(LucideSquarePi)
export const SquareTerminal = icon(LucideSquareTerminal)
export const Terminal = icon(LucideTerminal)
export const TriangleAlert = icon(LucideTriangleAlert)
export const Users = icon(LucideUsers)
export const Wrench = icon(LucideWrench)
export const X = icon(LucideX)
export const Zap = icon(LucideZap)
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
