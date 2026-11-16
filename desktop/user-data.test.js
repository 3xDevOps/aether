'use strict'

const assert = require('node:assert/strict')
const fs = require('node:fs')
const Module = require('node:module')
const os = require('node:os')
const path = require('node:path')
const { after, test } = require('node:test')

const { chooseUserData } = require('./user-data')

const roots = []
after(() => {
  for (const root of roots) fs.rmSync(root, { recursive: true, force: true })
})

function appDataWith(...dirs) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'user-data-'))
  roots.push(root)
  for (const dir of dirs) fs.mkdirSync(path.join(root, dir))
  return root
}

test('an app with a new name uses its own directory when it exists', () => {
  const root = appDataWith('cloudfield-desktop', 'aether-desktop')
  assert.equal(chooseUserData(root, 'cloudfield-desktop'), path.join(root, 'cloudfield-desktop'))
})

test('an app with a new name uses the old directory when only that exists', () => {
  const root = appDataWith('aether-desktop')
  assert.equal(chooseUserData(root, 'cloudfield-desktop'), path.join(root, 'aether-desktop'))
})

test('an app with a new name uses its own directory when neither exists', () => {
  const root = appDataWith()
  assert.equal(chooseUserData(root, 'cloudfield-desktop'), path.join(root, 'cloudfield-desktop'))
})

test('the app under its current name keeps its directory', () => {
  for (const root of [appDataWith('aether-desktop'), appDataWith()]) {
    assert.equal(chooseUserData(root, 'aether-desktop'), path.join(root, 'aether-desktop'))
  }
})

// main.js against a stand-in electron that creates userData when asked for
// it, as Electron's path provider does: the choice has to be made, and the
// path set, before anything reads userData or takes the single-instance lock.
function loadMain(appData, name) {
  const calls = []
  const electron = {
    app: {
      name,
      getPath(key) {
        calls.push('getPath ' + key)
        if (key === 'appData') return appData
        if (key === 'userData') {
          const dir = path.join(appData, name)
          fs.mkdirSync(dir, { recursive: true })
          return dir
        }
        throw new Error('unexpected getPath ' + key)
      },
      setPath(key, dir) {
        if (!fs.existsSync(dir)) throw new Error('Failed to set path')
        calls.push(`setPath ${key} ${path.basename(dir)}`)
      },
      requestSingleInstanceLock() {
        calls.push('lock')
        return false
      },
      quit() {},
    },
  }
  const load = Module._load
  Module._load = function (request, ...rest) {
    return request === 'electron' ? electron : load.call(this, request, ...rest)
  }
  try {
    delete require.cache[require.resolve('./main')]
    require('./main')
  } finally {
    Module._load = load
  }
  return calls
}

test('main.js picks the old directory before the lock and before reading userData', () => {
  const root = appDataWith('aether-desktop')
  assert.deepEqual(loadMain(root, 'cloudfield-desktop'), ['getPath appData', 'setPath userData aether-desktop', 'lock'])
  assert.equal(fs.existsSync(path.join(root, 'cloudfield-desktop')), false)
})

test('main.js leaves the data directory alone under its current name', () => {
  for (const root of [appDataWith('aether-desktop'), appDataWith()]) {
    assert.deepEqual(loadMain(root, 'aether-desktop'), ['getPath appData', 'lock'])
  }
})
