'use strict'

const assert = require('node:assert/strict')
const { test } = require('node:test')

const { relaunchOptions, relaunchTarget, sidecarExitAction } = require('./sidecar-exit')

test('75 relaunches the shell and 76 quits it for the updater', () => {
  assert.equal(sidecarExitAction(75), 'relaunch')
  assert.equal(sidecarExitAction(76), 'quit')
})

test('every other exit respawns the sidecar', () => {
  for (const code of [0, 1, 2, 74, 77, 130, null]) {
    assert.equal(sidecarExitAction(code), 'respawn', String(code))
  }
})

test('a relaunch line names the app the rebuild installed', () => {
  const exe = '/Applications/Cloudfield.app/Contents/MacOS/Cloudfield'
  assert.equal(relaunchTarget(JSON.stringify({ relaunch: exe })), exe)
})

test('anything else names no relaunch target', () => {
  for (const line of ['', 'not json', '{"url":"http://127.0.0.1:1/?token=x"}', '{"relaunch":"relative/app"}', '{"relaunch":7}', 'null']) {
    assert.equal(relaunchTarget(line), '', line)
  }
})

// A stand-in realpath over the paths that exist, with one symlink.
function realpathOver(...existing) {
  return (p) => {
    const resolved = p === '/usr/local/bin/app-link' ? '/opt/app/aether-desktop' : p
    if (!existing.includes(resolved)) throw new Error('ENOENT: ' + p)
    return resolved
  }
}

const argv = ['/opt/app/aether-desktop', '--no-sandbox', 'aether://run/run-7k2m9q4xbd']

test('relaunching this shell keeps its whole command line', () => {
  const realpath = realpathOver('/opt/app/aether-desktop')
  assert.equal(relaunchOptions('', '/opt/app/aether-desktop', argv, 'linux', realpath), null)
  assert.equal(relaunchOptions('/opt/app/aether-desktop', '/opt/app/aether-desktop', argv, 'linux', realpath), null)
  assert.equal(relaunchOptions('/opt/app/aether-desktop', '/usr/local/bin/app-link', argv, 'linux', realpath), null)
})

test('a case-insensitive filesystem matches this shell in another case', () => {
  const exe = '/Applications/Aether.app/Contents/MacOS/Aether'
  const realpath = (p) => p
  assert.equal(relaunchOptions(exe.toLowerCase(), exe, [exe], 'darwin', realpath), null)
  assert.notEqual(relaunchOptions(exe.toLowerCase(), exe, [exe], 'linux', realpath), null)
})

test('relaunching another app passes this shell its arguments', () => {
  const other = '/home/u/.local/share/cloudfield/desktop/aether-desktop'
  const realpath = realpathOver('/opt/app/aether-desktop', other)
  assert.deepEqual(relaunchOptions(other, '/opt/app/aether-desktop', argv, 'linux', realpath), {
    execPath: other,
    args: ['--no-sandbox', 'aether://run/run-7k2m9q4xbd'],
  })
})

test('a relaunch target that does not exist relaunches this shell', () => {
  const realpath = realpathOver('/opt/app/aether-desktop')
  assert.equal(relaunchOptions('/gone/aether-desktop', '/opt/app/aether-desktop', argv, 'linux', realpath), null)
})
