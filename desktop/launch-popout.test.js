'use strict'

const assert = require('node:assert/strict')
const test = require('node:test')
const { isLaunchPopoutURL } = require('./launch-popout')

const gateway = 'http://127.0.0.1:4000/?token=secret'

test('allows only the tokened launch pop-out on the gateway origin and path', () => {
  assert.equal(isLaunchPopoutURL('http://127.0.0.1:4000/?token=secret&new=1&popout=1', gateway), true)
  assert.equal(isLaunchPopoutURL('http://127.0.0.1:4000/?token=secret&new=1', gateway), false)
  assert.equal(isLaunchPopoutURL('http://127.0.0.1:4000/other?token=secret&new=1&popout=1', gateway), false)
  assert.equal(isLaunchPopoutURL('http://127.0.0.1:4000/?token=other&new=1&popout=1', gateway), false)
  assert.equal(isLaunchPopoutURL('https://example.com/?token=secret&new=1&popout=1', gateway), false)
})
