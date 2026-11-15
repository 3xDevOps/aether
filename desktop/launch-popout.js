'use strict'

function isLaunchPopoutURL(target, gatewayURL) {
  try {
    const url = new URL(target)
    const gateway = new URL(gatewayURL)
    const token = gateway.searchParams.get('token')
    return Boolean(token) && url.origin === gateway.origin && url.pathname === gateway.pathname &&
      url.searchParams.get('token') === token && url.searchParams.get('new') === '1' && url.searchParams.get('popout') === '1'
  } catch {
    return false
  }
}

module.exports = { isLaunchPopoutURL }
