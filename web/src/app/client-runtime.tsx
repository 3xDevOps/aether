'use client'

import dynamic from 'next/dynamic'

// App owns the existing live store, xterm host, Electron bridge, and gateway
// connection. Keep it behind a client-only dynamic boundary so static export
// does not evaluate browser-only modules during server prerendering.
const App = dynamic(() => import('@/App').then(({ App: DashboardApp }) => DashboardApp), {
  ssr: false,
})

export default function ClientRuntime() {
  return <App />
}
