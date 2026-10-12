'use strict'
const secret = location.hash
const open = document.getElementById('open')
const link = document.getElementById('link')
open.href += secret
link.value += secret
document.getElementById('copy').addEventListener('click', (event) => {
  navigator.clipboard.writeText(link.value).then(() => {
    event.target.textContent = 'Copied'
  })
})
if (secret) location.replace(open.href)
