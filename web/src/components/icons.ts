import { createElement, forwardRef } from 'react'
import {
  ArrowLeft as LucideArrowLeft,
  ArrowUp as LucideArrowUp,
  Bot as LucideBot,
  Check as LucideCheck,
  ChevronDown as LucideChevronDown,
  ChevronRight as LucideChevronRight,
  ChevronLeft as LucideChevronLeft,
  ChevronUp as LucideChevronUp,
  Clock as LucideClock,
  Copy as LucideCopy,
  Ellipsis as LucideEllipsis,
  FileText as LucideFileText,
  Folder as LucideFolder,
  FolderGit2 as LucideFolderGit2,
  GitBranch as LucideGitBranch,
  GitPullRequest as LucideGitPullRequest,
  History as LucideHistory,
  Keyboard as LucideKeyboard,
  ListTodo as LucideListTodo,
  LoaderCircle as LucideLoaderCircle,
  Lock as LucideLock,
  Mic as LucideMic,
  MessageSquare as LucideMessageSquare,
  PanelLeft as LucidePanelLeft,
  Pause as LucidePause,
  Play as LucidePlay,
  Plus as LucidePlus,
  RefreshCw as LucideRefreshCw,
  Search as LucideSearch,
  Settings as LucideSettings,
  Shield as LucideShield,
  Sparkles as LucideSparkles,
  Square as LucideSquare,
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
  FoldVertical as LucideFoldVertical,
  ListFilter as LucideListFilter,
  CircleHelp as LucideCircleHelp,
  Reply as LucideReply,
  ArrowRight as LucideArrowRight,
  WrapText as LucideWrapText,
  Pencil as LucidePencil,
  Globe as LucideGlobe,
  Trash2 as LucideTrash2,
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
  ZoomIn as LucideZoomIn,
  PackageX as LucidePackageX,
  Rocket as LucideRocket,
  ServerCog as LucideServerCog,
  ServerOff as LucideServerOff,
  ShieldOff as LucideShieldOff,
  Sun as LucideSun,
  Unplug as LucideUnplug,
  UserPlus as LucideUserPlus,
  WifiOff as LucideWifiOff,
  Columns2 as LucideColumns2,
  Columns3 as LucideColumns3,
  Network as LucideNetwork,
  ChartGantt as LucideChartGantt,
  MousePointer2 as LucideMousePointer2,
  RotateCw as LucideRotateCw,
  Smartphone as LucideSmartphone,
  Tablet as LucideTablet,
  Bug as LucideBug,
  FileSymlink as LucideFileSymlink,
  ChartColumn as LucideChartColumn,
  BookMarked as LucideBookMarked,
  Pin as LucidePin,
  PinOff as LucidePinOff,
  Undo2 as LucideUndo2,
  Bell as LucideBell,
  Eye as LucideEye,
  EyeOff as LucideEyeOff,
  Gauge as LucideGauge,
  Upload as LucideUpload,
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

function mark(name: string, viewBox: string, paths: string[]): LucideIcon {
  const Mark = forwardRef<SVGSVGElement, LucideProps>((props, ref) =>
    createElement(
      'svg',
      { viewBox, width: 24, height: 24, fill: 'currentColor', ...props, ref },
      ...paths.map((d) => createElement('path', { d })),
    ),
  )
  Mark.displayName = name
  return Mark
}

export const Claude = mark('Claude', '0 0 24 24', ['m4.7144 15.9555 4.7174-2.6471.079-.2307-.079-.1275h-.2307l-.7893-.0486-2.6956-.0729-2.3375-.0971-2.2646-.1214-.5707-.1215-.5343-.7042.0546-.3522.4797-.3218.686.0608 1.5179.1032 2.2767.1578 1.6514.0972 2.4468.255h.3886l.0546-.1579-.1336-.0971-.1032-.0972L6.973 9.8356l-2.55-1.6879-1.3356-.9714-.7225-.4918-.3643-.4614-.1578-1.0078.6557-.7225.8803.0607.2246.0607.8925.686 1.9064 1.4754 2.4893 1.8336.3643.3035.1457-.1032.0182-.0728-.164-.2733-1.3539-2.4467-1.445-2.4893-.6435-1.032-.17-.6194c-.0607-.255-.1032-.4674-.1032-.7285L6.287.1335 6.6997 0l.9957.1336.419.3642.6192 1.4147 1.0018 2.2282 1.5543 3.0296.4553.8985.2429.8318.091.255h.1579v-.1457l.1275-1.706.2368-2.0947.2307-2.6957.0789-.7589.3764-.9107.7468-.4918.5828.2793.4797.686-.0668.4433-.2853 1.8517-.5586 2.9021-.3643 1.9429h.2125l.2429-.2429.9835-1.3053 1.6514-2.0643.7286-.8196.85-.9046.5464-.4311h1.0321l.759 1.1293-.34 1.1657-1.0625 1.3478-.8804 1.1414-1.2628 1.7-.7893 1.36.0729.1093.1882-.0183 2.8535-.607 1.5421-.2794 1.8396-.3157.8318.3886.091.3946-.3278.8075-1.967.4857-2.3072.4614-3.4364.8136-.0425.0304.0486.0607 1.5482.1457.6618.0364h1.621l3.0175.2247.7892.522.4736.6376-.079.4857-1.2142.6193-1.6393-.3886-3.825-.9107-1.3113-.3279h-.1822v.1093l1.0929 1.0686 2.0035 1.8092 2.5075 2.3314.1275.5768-.3218.4554-.34-.0486-2.2039-1.6575-.85-.7468-1.9246-1.621h-.1275v.17l.4432.6496 2.3436 3.5214.1214 1.0807-.17.3521-.6071.2125-.6679-.1214-1.3721-1.9246L14.38 17.959l-1.1414-1.9428-.1397.079-.674 7.2552-.3156.3703-.7286.2793-.6071-.4614-.3218-.7468.3218-1.4753.3886-1.9246.3157-1.53.2853-1.9004.17-.6314-.0121-.0425-.1397.0182-1.4328 1.9672-2.1796 2.9446-1.7243 1.8456-.4128.164-.7164-.3704.0667-.6618.4008-.5889 2.386-3.0357 1.4389-1.882.929-1.0868-.0062-.1579h-.0546l-6.3385 4.1164-1.1293.1457-.4857-.4554.0608-.7467.2307-.2429 1.9064-1.3114Z'])
export const OpenAI = mark('OpenAI', '180.5 180.5 354.674 354.674', ['M508.749 317.399C516.777 287.314 508.991 253.884 485.389 230.282C461.788 206.681 428.36 198.895 398.273 206.923C376.231 184.928 343.39 174.956 311.148 183.596C278.906 192.234 255.45 217.292 247.36 247.361C217.291 255.451 192.233 278.91 183.595 311.149C174.957 343.391 184.927 376.232 206.924 398.274C198.896 428.359 206.683 461.789 230.284 485.391C253.885 508.992 287.313 516.779 317.401 508.75C339.442 530.745 372.286 540.717 404.525 532.079C436.767 523.441 460.223 498.384 468.313 468.315C498.383 460.224 523.44 436.766 532.078 404.526C540.716 372.285 530.747 339.443 508.749 317.402V317.399ZM470.899 244.776C486.892 260.77 493.488 282.601 490.687 303.412L415.577 260.046C412.411 258.218 408.509 258.218 405.345 260.046L317.401 310.82V277.526C317.401 275.191 318.652 273.005 320.676 271.837L387.644 233.174C414.178 218.353 448.346 222.223 470.901 244.776H470.899ZM357.837 311.144L398.275 334.491V381.185L357.837 404.532L317.398 381.185V334.491L357.837 311.144ZM264.776 269.693C265.207 239.305 285.644 211.649 316.453 203.393C338.3 197.54 360.505 202.744 377.127 215.573L302.014 258.937C298.848 260.764 296.898 264.144 296.898 267.798V369.346L268.065 352.699C266.043 351.531 264.776 349.353 264.776 347.017V269.691V269.693ZM203.391 316.454C209.244 294.608 224.854 277.978 244.276 269.999V356.73C244.276 360.384 246.226 363.763 249.392 365.591L337.337 416.365L308.503 433.013C306.481 434.181 303.961 434.188 301.939 433.02L234.971 394.357C208.868 378.789 195.138 347.261 203.391 316.454ZM244.775 470.9C228.781 454.906 222.186 433.075 224.986 412.264L300.096 455.63C303.263 457.457 307.164 457.457 310.328 455.63L398.273 404.856V438.149C398.273 440.485 397.022 442.671 394.997 443.839L328.029 482.502C301.495 497.322 267.327 493.452 244.772 470.9H244.775ZM450.897 445.982C450.466 476.371 430.029 504.027 399.22 512.283C377.373 518.136 355.168 512.932 338.547 500.102L413.659 456.738C416.826 454.911 418.775 451.532 418.775 447.877V346.329L447.609 362.977C449.631 364.145 450.897 366.323 450.897 368.659V445.985V445.982ZM512.282 399.221C506.429 421.068 490.819 437.697 471.397 445.676V358.946C471.397 355.292 469.448 351.912 466.281 350.085L378.336 299.311L407.17 282.663C409.192 281.495 411.712 281.487 413.734 282.655L480.702 321.318C506.805 336.887 520.536 368.415 512.282 399.221Z'])
export const Pi = mark('Pi', '0 0 24 24', ['M0 0v24h6v-6h6v-6H6V6h6v6h6V0Zm18 12v12h6V12Z'])
export const OhMyPi = mark('OhMyPi', '14 16 36 40', ['M14 16h36v8H40v32h-8V24h-6v22h-8V24h-4z'])
export const OpenCode = mark('OpenCode', '0 0 240 300', ['M180 60H60V240H180V60ZM240 300H0V0H240V300Z'])
export const GitHub = mark('GitHub', '0 0 24 24', ['M10.226 17.284c-2.965-.36-5.054-2.493-5.054-5.256 0-1.123.404-2.336 1.078-3.144-.292-.741-.247-2.314.09-2.965.898-.112 2.111.36 2.83 1.01.853-.269 1.752-.404 2.853-.404 1.1 0 1.999.135 2.807.382.696-.629 1.932-1.1 2.83-.988.315.606.36 2.179.067 2.942.72.854 1.101 2 1.101 3.167 0 2.763-2.089 4.852-5.098 5.234.763.494 1.28 1.572 1.28 2.807v2.336c0 .674.561 1.056 1.235.786 4.066-1.55 7.255-5.615 7.255-10.646C23.5 6.188 18.334 1 11.978 1 5.62 1 .5 6.188.5 12.545c0 4.986 3.167 9.12 7.435 10.669.606.225 1.19-.18 1.19-.786V20.63a2.9 2.9 0 0 1-1.078.224c-1.483 0-2.359-.808-2.987-2.313-.247-.607-.517-.966-1.034-1.033-.27-.023-.359-.135-.359-.27 0-.27.45-.471.898-.471.652 0 1.213.404 1.797 1.235.45.651.921.943 1.483.943.561 0 .92-.202 1.437-.719.382-.381.674-.718.944-.943'])
export const Cursor = mark('Cursor', '0 0 466.73 532.09', ['M457.43,125.94L244.42,2.96c-6.84-3.95-15.28-3.95-22.12,0L9.3,125.94c-5.75,3.32-9.3,9.46-9.3,16.11v247.99c0,6.65,3.55,12.79,9.3,16.11l213.01,122.98c6.84,3.95,15.28,3.95,22.12,0l213.01-122.98c5.75-3.32,9.3-9.46,9.3-16.11v-247.99c0-6.65-3.55-12.79-9.3-16.11h-.01ZM444.05,151.99l-205.63,356.16c-1.39,2.4-5.06,1.42-5.06-1.36v-233.21c0-4.66-2.49-8.97-6.53-11.31L24.87,145.67c-2.4-1.39-1.42-5.06,1.36-5.06h411.26c5.84,0,9.49,6.33,6.57,11.39h-.01Z'])
export const Gemini = mark('Gemini', '0 0 512 512', ['M419.776 0.00781248C470.82 0.654592 512 42.2332 512 93.4296V418.572L511.992 419.776C511.344 470.82 469.768 512 418.572 512H93.4296C41.83 512 0.00052574 470.168 0 418.572V93.4296C0.000523248 42.2332 41.1792 0.654596 92.2228 0.00781248L93.4296 0H418.572L419.776 0.00781248ZM93.4296 29.8984C58.3424 29.899 29.899 58.3424 29.8984 93.4296V418.572C29.899 453.656 58.3424 482.1 93.4296 482.1H418.572C453.656 482.1 482.1 453.656 482.1 418.572V93.4296C482.1 58.3424 453.656 29.899 418.572 29.8984H93.4296ZM357.926 223.129V301.274L154.754 398.949V342.297L321.199 262.199L154.754 182.106V125.453L357.926 223.129Z'])
export const Antigravity = mark('Antigravity', '0 0 180 180', ['M144.248 149.062C151.748 154.688 162.998 150.938 152.685 140.625C121.748 110.625 128.31 28.125 89.8727 28.125C51.4352 28.125 57.9977 110.625 27.0602 140.625C15.8102 151.875 27.9977 154.688 35.4977 149.062C64.5602 129.375 62.6852 94.6875 89.8727 94.6875C117.06 94.6875 115.185 129.375 144.248 149.062Z'])

export const ArrowLeft = icon(LucideArrowLeft)
export const ArrowUp = icon(LucideArrowUp)
export const Bot = icon(LucideBot)
export const Check = icon(LucideCheck)
export const ChevronDown = icon(LucideChevronDown)
export const ChevronRight = icon(LucideChevronRight)
export const ChevronUp = icon(LucideChevronUp)
export const Clock = icon(LucideClock)
export const Copy = icon(LucideCopy)
export const Ellipsis = icon(LucideEllipsis)
export const FileText = icon(LucideFileText)
export const FolderGit2 = icon(LucideFolderGit2)
export const GitBranch = icon(LucideGitBranch)
export const GitPullRequest = icon(LucideGitPullRequest)
export const History = icon(LucideHistory)
export const ChartColumn = icon(LucideChartColumn)
export const BookMarked = icon(LucideBookMarked)
export const Pin = icon(LucidePin)
export const PinOff = icon(LucidePinOff)
export const Undo2 = icon(LucideUndo2)
export const Bell = icon(LucideBell)
export const Eye = icon(LucideEye)
export const EyeOff = icon(LucideEyeOff)
export const Gauge = icon(LucideGauge)
export const Upload = icon(LucideUpload)
export const Keyboard = icon(LucideKeyboard)
export const ListTodo = icon(LucideListTodo)
export const LoaderCircle = icon(LucideLoaderCircle)
export const Lock = icon(LucideLock)
export const Mic = icon(LucideMic)
export const MessageSquare = icon(LucideMessageSquare)
export const PanelLeft = icon(LucidePanelLeft)
export const Pause = icon(LucidePause)
export const Play = icon(LucidePlay)
export const Plus = icon(LucidePlus)
export const RefreshCw = icon(LucideRefreshCw)
export const Search = icon(LucideSearch)
export const Settings = icon(LucideSettings)
export const Shield = icon(LucideShield)
/** Interrupt. */
export const Sparkles = icon(LucideSparkles)
export const Square = icon(LucideSquare)
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
export const FoldVertical = icon(LucideFoldVertical)
export const ListFilter = icon(LucideListFilter)
export const CircleHelp = icon(LucideCircleHelp)
export const Reply = icon(LucideReply)
export const ArrowRight = icon(LucideArrowRight)
export const Pencil = icon(LucidePencil)
export const Globe = icon(LucideGlobe)
export const Trash2 = icon(LucideTrash2)
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
export const ZoomIn = icon(LucideZoomIn)
export const PackageX = icon(LucidePackageX)
export const Rocket = icon(LucideRocket)
export const ServerCog = icon(LucideServerCog)
export const ServerOff = icon(LucideServerOff)
export const ShieldOff = icon(LucideShieldOff)
export const Sun = icon(LucideSun)
export const Unplug = icon(LucideUnplug)
export const UserPlus = icon(LucideUserPlus)
export const WifiOff = icon(LucideWifiOff)
export const Columns2 = icon(LucideColumns2)
export const Columns3 = icon(LucideColumns3)
export const Network = icon(LucideNetwork)
export const ChartGantt = icon(LucideChartGantt)
export const MousePointer2 = icon(LucideMousePointer2)
export const RotateCw = icon(LucideRotateCw)
export const Smartphone = icon(LucideSmartphone)
export const Tablet = icon(LucideTablet)
export const Bug = icon(LucideBug)
export const FileSymlink = icon(LucideFileSymlink)
