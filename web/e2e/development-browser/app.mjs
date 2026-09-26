import { createServer } from 'node:http'
import { randomUUID } from 'node:crypto'
import { readFile, watch } from 'node:fs/promises'

// A real app, not an Aether API mock. Its only listener is in the run's
// network namespace. Sessions survive page reloads and module hot updates.
const sessions = new Set()
const subscribers = new Set()
const root = new URL('./', import.meta.url)
const html = `<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Shared login app</title>
<style>
*{box-sizing:border-box}[hidden]{display:none!important}body{margin:0;padding:20px;font:18px sans-serif;background:#f5f5f5;color:#17202a}h1{position:absolute;top:20px;font-size:24px;margin:0}#width{position:absolute;top:60px}label{display:block}input,button{display:block;height:40px;width:300px;max-width:calc(100vw - 40px);font:18px sans-serif;margin:0}button{background:#174ea6;color:white;border:0}#login label{position:absolute;top:85px}#login label:nth-child(2){top:160px}#login input{margin-top:4px}#login button{position:absolute;top:245px}#result{position:absolute;top:295px;margin:0}#logout{position:absolute;top:345px}#note-label{position:absolute;top:400px}#note{margin-top:4px}#echo{position:absolute;top:470px}a{position:absolute;top:530px}#hot{position:absolute;top:570px;color:#136f2d}#scroll{position:absolute;top:1400px}
#touch{position:absolute;top:630px;width:300px;height:100px;background:#ddd;touch-action:none}
#scroll-state{position:fixed;right:20px;bottom:20px;background:white}
</style>
<h1 id="title">Shared login app</h1><div id="width"></div>
<form id="login"><label>Email<input name="email" aria-label="Email" autocomplete="username"></label><label>Password<input name="password" aria-label="Password" type="password" autocomplete="current-password"></label><button>Sign in</button></form>
<p id="result" role="status"></p><button id="logout" hidden>Log out</button>
<label id="note-label">Shared note<input id="note" aria-label="Shared note"></label><p id="echo"></p><a href="/popup" target="_blank">Open popup</a><p id="hot"></p><p id="scroll">Scrolled to the bottom</p>
<div id="touch">Touch pad</div>
<output id="scroll-state">Scroll 0</output>
<script type="module">
const login=document.querySelector('#login'), result=document.querySelector('#result'), logout=document.querySelector('#logout');
const render=async()=>{const state=await (await fetch('/session')).json();login.hidden=state.signed_in;logout.hidden=!state.signed_in;result.textContent=state.signed_in?'Signed in as test@example.invalid':'Signed out';};
login.addEventListener('submit',async(event)=>{event.preventDefault();const response=await fetch('/login',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(Object.fromEntries(new FormData(login)))});if(!response.ok){result.textContent='Invalid credentials';return;}await render();});
logout.addEventListener('click',async()=>{await fetch('/logout',{method:'POST'});await render();});
document.querySelector('#note').addEventListener('input',(event)=>{document.querySelector('#echo').textContent='Note: '+event.target.value;});
document.querySelector('#touch').addEventListener('touchstart',(event)=>{event.currentTarget.textContent='Touches '+event.touches.length;});
document.querySelector('#touch').addEventListener('dblclick',(event)=>{event.currentTarget.textContent='Double click received';});
addEventListener('scroll',()=>{document.querySelector('#scroll-state').textContent='Scroll '+Math.round(scrollY);});
const width=()=>document.querySelector('#width').textContent='Viewport '+innerWidth;addEventListener('resize',width);width();
const hot=async()=>{const module=await import('/theme.mjs?revision='+Date.now());document.querySelector('#title').textContent=module.title;document.querySelector('#hot').textContent='Module: '+module.title;};
new EventSource('/updates').addEventListener('message',hot);await hot();await render();
</script>`
const server = createServer(async (request, response) => {
  try {
    const url = new URL(request.url, 'http://localhost')
    const cookie = /(?:^|;\s*)session=([^;]+)/.exec(request.headers.cookie ?? '')?.[1]
    response.setHeader('Cache-Control', 'no-store')
    if (url.pathname === '/session') {
      response.setHeader('Content-Type', 'application/json')
      response.end(JSON.stringify({ signed_in: sessions.has(cookie) }))
    } else if (url.pathname === '/login' && request.method === 'POST') {
      let body = ''
      for await (const bytes of request) {
        body += bytes
        if (body.length > 4096) { response.writeHead(413).end(); return }
      }
      const credentials = JSON.parse(body)
      if (credentials.email !== 'test@example.invalid' || credentials.password !== 'correct horse') { response.writeHead(401).end('Invalid credentials'); return }
      const session = randomUUID()
      sessions.add(session)
      response.setHeader('Set-Cookie', `session=${session}; HttpOnly; SameSite=Strict; Path=/`)
      response.end('Signed in')
    } else if (url.pathname === '/logout' && request.method === 'POST') {
      sessions.delete(cookie)
      response.setHeader('Set-Cookie', 'session=; HttpOnly; SameSite=Strict; Path=/; Max-Age=0')
      response.end('Signed out')
    } else if (url.pathname === '/theme.mjs') {
      response.setHeader('Content-Type', 'text/javascript')
      response.end(await readFile(new URL('theme.mjs', root)))
    } else if (url.pathname === '/updates') {
      response.writeHead(200, { 'Content-Type': 'text/event-stream', Connection: 'keep-alive' })
      response.write(': connected\n\n')
      subscribers.add(response)
      response.on('close', () => subscribers.delete(response))
    } else if (url.pathname === '/popup') {
      response.setHeader('Content-Type', 'text/html')
      response.end('<!doctype html><title>Authentication popup</title><h1>Shared popup</h1><button onclick="window.close()">Close popup</button>')
    } else if (url.pathname === '/') {
      response.setHeader('Content-Type', 'text/html')
      response.end(html)
    } else { response.writeHead(404).end('Not found') }
  } catch (error) { response.writeHead(500).end(String(error)) }
})
server.listen(31873, '127.0.0.1', () => console.log('loopback-login-ready'))
for await (const event of watch(root)) {
  if (event.filename === 'theme.mjs') for (const subscriber of subscribers) subscriber.write('data: source changed\n\n')
}
