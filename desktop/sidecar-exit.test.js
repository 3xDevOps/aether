'use strict'

const assert = require('node:assert/strict')
const { test } = require('node:test')

const { relaunchTarget, sidecarExitAction } = require('./sidecar-exit')

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
