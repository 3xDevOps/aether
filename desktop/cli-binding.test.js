'use strict'

const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { test } = require('node:test')

const { boundCli } = require('./cli-binding')

function resources(t, contents) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'aether-binding-'))
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }))
  if (contents !== undefined) fs.writeFileSync(path.join(dir, 'aether-cli.json'), contents)
  return dir
}

const cli = path.resolve('/opt/aether/bin/aether')

test('the binding file wins over the path an older build stamped', (t) => {
  const dir = resources(t, JSON.stringify({ path: cli }))
  assert.equal(boundCli(dir, { aetherCliPath: path.resolve('/old/aether') }), cli)
})

test('without a binding file the stamped path is used', (t) => {
  const dir = resources(t)
  assert.equal(boundCli(dir, { aetherCliPath: cli }), cli)
  assert.equal(boundCli(dir, {}), '')
})

test('a binding file without an absolute path is an error, not a fallback', (t) => {
  assert.throws(() => boundCli(resources(t, '{"path":"aether"}'), { aetherCliPath: cli }), /absolute path/)
  assert.throws(() => boundCli(resources(t, 'not json'), {}), /aether-cli\.json: Unexpected token/)
})
