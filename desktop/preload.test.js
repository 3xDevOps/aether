'use strict'

const assert = require('node:assert/strict')
const Module = require('node:module')
const { test, beforeEach } = require('node:test')

const sent = []
let keydown = null
globalThis.window = { addEventListener: (type, listener) => { if (type === 'keydown') keydown = listener } }

const load = Module._load
Module._load = function (request, ...rest) {
  if (request === 'electron') {
    return {
      contextBridge: { exposeInMainWorld: () => {} },
      ipcRenderer: { send: (...args) => sent.push(args), invoke: () => {}, on: () => {}, removeListener: () => {} },
    }
  }
  return load.call(this, request, ...rest)
}
require('./preload')

const platform = Object.getOwnPropertyDescriptor(process, 'platform')
function on(name) {
  Object.defineProperty(process, 'platform', { ...platform, value: name })
}

function press(key, modifiers = {}) {
  sent.length = 0
  const event = {
    key,
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    defaultPrevented: false,
    ...modifiers,
    preventDefault() { this.defaultPrevented = true },
  }
  keydown(event)
  assert.equal(event.defaultPrevented, sent.length > 0 || Boolean(modifiers.defaultPrevented))
  return sent.length ? sent[0][1] : null
}

beforeEach(() => on(platform.value))

test('Ctrl zooms the window on Windows and Linux', () => {
  for (const name of ['win32', 'linux']) {
    on(name)
    assert.equal(press('=', { ctrlKey: true }), 'in')
    assert.equal(press('+', { ctrlKey: true }), 'in')
    assert.equal(press('-', { ctrlKey: true }), 'out')
    assert.equal(press('0', { ctrlKey: true }), 'reset')
    assert.equal(press('=', { metaKey: true }), null)
  }
})

test('Cmd zooms the window on macOS and Ctrl does not', () => {
  on('darwin')
  assert.equal(press('+', { metaKey: true }), 'in')
  assert.equal(press('0', { metaKey: true }), 'reset')
  assert.equal(press('=', { ctrlKey: true }), null)
})

test('leaves keys the page claimed or that mean something else', () => {
  on('linux')
  assert.equal(press('=', { ctrlKey: true, defaultPrevented: true }), null)
  // Ctrl+_ is readline's undo; keypad 0 with Num Lock off is Insert, and
  // Ctrl+Insert is copy; Ctrl+Alt is AltGr on Windows.
  assert.equal(press('_', { ctrlKey: true }), null)
  assert.equal(press('Insert', { ctrlKey: true }), null)
  assert.equal(press('=', { ctrlKey: true, altKey: true }), null)
  assert.equal(press('='), null)
  assert.equal(press('a', { ctrlKey: true }), null)
})
