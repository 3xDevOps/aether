'use strict'
const secret = location.hash
const open = document.getElementById('open')
const link = document.getElementById('link')
open.href += secret
link.value += secret
document.getElementById('copy').addEventListener('click', (event) => {
  const failed = () => {
    link.select()
    event.target.textContent = 'Select and copy'
  }
  if (!navigator.clipboard) return failed()
  navigator.clipboard.writeText(link.value).then(() => {
    event.target.textContent = 'Copied'
  }, failed)
})
if (secret) location.replace(open.href)
