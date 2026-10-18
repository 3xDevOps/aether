'use strict'

const assert = require('node:assert/strict')
const { test } = require('node:test')

const { sidecarExitAction } = require('./sidecar-exit')

test('75 relaunches the shell and 76 quits it for the updater', () => {
  assert.equal(sidecarExitAction(75), 'relaunch')
  assert.equal(sidecarExitAction(76), 'quit')
})

test('every other exit respawns the sidecar', () => {
  for (const code of [0, 1, 2, 74, 77, 130, null]) {
    assert.equal(sidecarExitAction(code), 'respawn', String(code))
  }
})
