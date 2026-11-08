'use strict'

const assert = require('node:assert/strict')
const Module = require('node:module')
const { test } = require('node:test')

let workArea = { x: 0, y: 0, width: 1920, height: 1080 }
const load = Module._load
Module._load = function (request, ...rest) {
  if (request === 'electron') return { screen: { getDisplayMatching: () => ({ workArea }) } }
  return load.call(this, request, ...rest)
}
const { zoomWindow } = require('./zoom')

const platform = Object.getOwnPropertyDescriptor(process, 'platform')
function on(name) {
  Object.defineProperty(process, 'platform', { ...platform, value: name })
}

function fakeWindow(bounds, { maximized = false } = {}) {
  let level = 0
  return {
    bounds,
    minimum: [960, 600],
    buttons: null,
    webContents: {
      getZoomLevel: () => level,
      setZoomLevel: (next) => { level = next },
      getZoomFactor: () => Math.pow(1.2, level),
    },
    getBounds() { return { ...this.bounds } },
    setBounds(next) { this.bounds = next },
    setMinimumSize(width, height) { this.minimum = [width, height] },
    isMaximized: () => maximized,
    isFullScreen: () => false,
    setWindowButtonPosition(point) { this.buttons = point },
    level: () => level,
  }
}

test('zooming in grows the minimum size and the window with it', () => {
  on('linux')
  workArea = { x: 0, y: 0, width: 1920, height: 1080 }
  const win = fakeWindow({ x: 1000, y: 0, width: 960, height: 600 })
  zoomWindow(win, 'in')
  zoomWindow(win, 'in')
  zoomWindow(win, 'in')
  assert.equal(win.level(), 1.5)
  const factor = Math.pow(1.2, 1.5)
  assert.deepEqual(win.minimum, [Math.ceil(960 * factor), Math.ceil(600 * factor)])
  // The CSS width stays at or above the 960px the layout is designed against.
  assert.ok(win.bounds.width / factor >= 960)
  // Grown in place, pulled back onto the work area.
  assert.equal(win.bounds.x, 1920 - win.bounds.width)
  zoomWindow(win, 'reset')
  assert.equal(win.level(), 0)
  assert.deepEqual(win.minimum, [960, 600])
})

test('refuses a zoom whose minimum size would not fit the display', () => {
  on('linux')
  workArea = { x: 0, y: 0, width: 1280, height: 800 }
  const win = fakeWindow({ x: 0, y: 0, width: 1280, height: 800 })
  for (let i = 0; i < 10; i++) zoomWindow(win, 'in')
  assert.equal(win.level(), 1.5)
  assert.ok(win.minimum[0] <= 1280 && win.minimum[1] <= 800)
})

test('zooms out on a display smaller than the zoomed minimum', () => {
  on('linux')
  workArea = { x: 0, y: 0, width: 1920, height: 1080 }
  const win = fakeWindow({ x: 0, y: 0, width: 1920, height: 1080 })
  for (let i = 0; i < 6; i++) zoomWindow(win, 'in')
  assert.equal(win.level(), 3)
  workArea = { x: 0, y: 0, width: 1280, height: 800 }
  zoomWindow(win, 'out')
  assert.equal(win.level(), 2.5)
  zoomWindow(win, 'in')
  assert.equal(win.level(), 2.5)
})

test('leaves a maximized window its size', () => {
  on('win32')
  workArea = { x: 0, y: 0, width: 1920, height: 1080 }
  const win = fakeWindow({ x: 0, y: 0, width: 1920, height: 1080 }, { maximized: true })
  zoomWindow(win, 'in')
  assert.deepEqual(win.bounds, { x: 0, y: 0, width: 1920, height: 1080 })
})

test('keeps the macOS traffic lights centred and clear of the logo', () => {
  on('darwin')
  workArea = { x: 0, y: 25, width: 1920, height: 1055 }
  const win = fakeWindow({ x: 0, y: 25, width: 1280, height: 840 })
  zoomWindow(win, 'in')
  assert.deepEqual(win.buttons, { x: 12, y: Math.round((44 * 1.2 ** 0.5 - 12) / 2) })
  zoomWindow(win, 'reset')
  assert.deepEqual(win.buttons, { x: 12, y: 16 })
  zoomWindow(win, 'out')
  zoomWindow(win, 'out')
  assert.equal(win.level(), -0.5)
})
