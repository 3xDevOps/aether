'use strict'

const assert = require('node:assert/strict')
const Module = require('node:module')
const { test, beforeEach } = require('node:test')

let level = 0
let keydown = null
globalThis.window = { addEventListener: (type, listener) => { if (type === 'keydown') keydown = listener } }

const load = Module._load
Module._load = function (request, ...rest) {
  if (request === 'electron') {
    return {
      contextBridge: { exposeInMainWorld: () => {} },
      ipcRenderer: { send: () => {}, invoke: () => {}, on: () => {}, removeListener: () => {} },
      webFrame: { getZoomLevel: () => level, setZoomLevel: (next) => { level = next } },
    }
  }
  return load.call(this, request, ...rest)
}
require('./preload')

const platform = Object.getOwnPropertyDescriptor(process, 'platform')
function on(name) {
  Object.defineProperty(process, 'platform', { ...platform, value: name })
}

function press(code, modifiers = {}) {
  const event = {
    code,
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    shiftKey: false,
    defaultPrevented: false,
    ...modifiers,
    preventDefault() { this.defaultPrevented = true },
  }
  keydown(event)
  return event.defaultPrevented
}

beforeEach(() => {
  level = 0
  on(platform.value)
})

test('Ctrl zooms the window in, out and back on Windows and Linux', () => {
  for (const name of ['win32', 'linux']) {
    on(name)
    level = 0
    assert.equal(press('Equal', { ctrlKey: true }), true)
    assert.equal(press('Equal', { ctrlKey: true, shiftKey: true }), true)
    assert.equal(press('NumpadAdd', { ctrlKey: true }), true)
    assert.equal(level, 1.5)
    assert.equal(press('Minus', { ctrlKey: true }), true)
    assert.equal(press('NumpadSubtract', { ctrlKey: true }), true)
    assert.equal(level, 0.5)
    assert.equal(press('Digit0', { ctrlKey: true }), true)
    assert.equal(level, 0)
    assert.equal(press('Equal', { metaKey: true }), false)
    assert.equal(level, 0)
  }
})

test('Cmd zooms the window on macOS and Ctrl does not', () => {
  on('darwin')
  assert.equal(press('Equal', { metaKey: true }), true)
  assert.equal(press('Equal', { ctrlKey: true }), false)
  assert.equal(level, 0.5)
  assert.equal(press('Numpad0', { metaKey: true }), true)
  assert.equal(level, 0)
})

test('leaves keys the page claimed or that mean something else', () => {
  on('linux')
  assert.equal(press('Equal', { ctrlKey: true, defaultPrevented: true }), true)
  assert.equal(press('Minus', { ctrlKey: true, shiftKey: true }), false)
  assert.equal(press('Digit0', { ctrlKey: true, shiftKey: true }), false)
  assert.equal(press('Equal', { ctrlKey: true, altKey: true }), false)
  assert.equal(press('Equal'), false)
  assert.equal(press('KeyA', { ctrlKey: true }), false)
  assert.equal(level, 0)
})
