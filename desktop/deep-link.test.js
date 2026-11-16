'use strict'

const assert = require('node:assert/strict')
const { test } = require('node:test')

const { deepLinkSuffix, findDeepLink } = require('./deep-link')

const edge = encodeURIComponent('https://edge.example.com')
const server = 'abcdefghijklmnopqrstuvwxyz'
const secret = '#abcdefghijk23456'

for (const scheme of ['aether', 'cloudfield']) {
  test(`${scheme}://run opens that run`, () => {
    assert.equal(deepLinkSuffix(`${scheme}://run/run-7k2m9q4xbd`), '&run=run-7k2m9q4xbd')
  })

  test(`${scheme}://join and claim reach the dashboard as aether:// links`, () => {
    const join = `?edge=${edge}&server=${server}&invitation=inv-7k2m9q4xbd${secret}`
    assert.equal(deepLinkSuffix(`${scheme}://join${join}`), '#join=' + encodeURIComponent(`aether://join${join}`))
    const claim = `?edge=${edge}&server=${server}${secret}`
    assert.equal(deepLinkSuffix(`${scheme}://claim${claim}`), '#claim=' + encodeURIComponent(`aether://claim${claim}`))
  })

  test(`${scheme}:// links that fail a check are refused`, () => {
    for (const link of [
      `${scheme}://run/RUN`,
      `${scheme}://other/run-7k2m9q4xbd`,
      `${scheme}://claim?edge=${encodeURIComponent('http://edge.example.com')}&server=${server}${secret}`,
      `${scheme}://claim?edge=${edge}&server=${server}`,
      `${scheme}://claim?edge=${edge}&server=${server}&extra=1${secret}`,
      `${scheme}://join?edge=${edge}&server=${server}&invitation=inv-bad${secret}`,
    ]) {
      assert.equal(deepLinkSuffix(link), '', link)
    }
  })

  test(`a ${scheme}:// link is found among argv`, () => {
    const link = `${scheme}://run/run-7k2m9q4xbd`
    assert.equal(findDeepLink(['/opt/app/aether-desktop', '--no-sandbox', link]), link)
  })
}

test('other schemes are refused', () => {
  assert.equal(deepLinkSuffix('https://run/run-7k2m9q4xbd'), '')
  assert.equal(deepLinkSuffix('aetherx://run/run-7k2m9q4xbd'), '')
  assert.equal(findDeepLink(['/opt/app/aether-desktop', 'https://example.com']), '')
})
