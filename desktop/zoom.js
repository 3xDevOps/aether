'use strict'

// Window zoom for the shell, which has no View menu to provide it.

const { screen } = require('electron')

// The floor the dashboard's own layout is designed against; see
// docs/dashboard-frontend.md.
const MIN_WIDTH = 960
const MIN_HEIGHT = 600
// Where the macOS traffic lights sit at 100%, centred in the 44px
// navigation row; they are 12px tall.
const TRAFFIC_LIGHTS = { x: 12, y: 16 }
const ROW_HEIGHT = 44
const LIGHT_HEIGHT = 12
// Electron's own zoom step, as in its zoomIn and zoomOut menu roles.
const STEP = 0.5
// The navigation row reserves 80 CSS px for the traffic lights, which keep
// their native size; below 0.5 levels out (91%) the logo runs into them.
const SMALLEST_DARWIN = -0.5
// Chromium stops shrinking the page at 25%.
const SMALLEST = Math.log(0.25) / Math.log(1.2)

// The window's minimum size grows with the zoom so the layout never drops
// below the width it is designed against, where the phone layout would
// take the title bar's window controls and drag region away. Zooming in
// past what the display's work area holds is refused; zooming out is not,
// so a window moved to a smaller display can still come back down.
function zoomWindow(target, step) {
  const contents = target.webContents
  let level = 0
  if (step !== 'reset') {
    level = contents.getZoomLevel() + (step === 'in' ? STEP : -STEP)
    const factor = Math.pow(1.2, level)
    const { workArea } = screen.getDisplayMatching(target.getBounds())
    const smallest = process.platform === 'darwin' ? SMALLEST_DARWIN : SMALLEST
    if (level < smallest) return
    if (step === 'in' && (MIN_WIDTH * factor > workArea.width || MIN_HEIGHT * factor > workArea.height)) return
  }
  contents.setZoomLevel(level)
  const factor = contents.getZoomFactor()
  const width = Math.ceil(MIN_WIDTH * factor)
  const height = Math.ceil(MIN_HEIGHT * factor)
  target.setMinimumSize(width, height)
  if (!target.isMaximized() && !target.isFullScreen()) {
    const bounds = target.getBounds()
    if (bounds.width < width || bounds.height < height) {
      const { workArea } = screen.getDisplayMatching(bounds)
      const grown = { width: Math.max(bounds.width, width), height: Math.max(bounds.height, height) }
      target.setBounds({
        ...grown,
        x: Math.max(workArea.x, Math.min(bounds.x, workArea.x + workArea.width - grown.width)),
        y: Math.max(workArea.y, Math.min(bounds.y, workArea.y + workArea.height - grown.height)),
      })
    }
  }
  if (process.platform === 'darwin') {
    target.setWindowButtonPosition({ x: TRAFFIC_LIGHTS.x, y: Math.round((ROW_HEIGHT * factor - LIGHT_HEIGHT) / 2) })
  }
}

module.exports = { MIN_WIDTH, MIN_HEIGHT, TRAFFIC_LIGHTS, zoomWindow }
