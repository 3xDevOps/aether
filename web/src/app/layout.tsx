import type { Metadata } from 'next'
import type { ReactNode } from 'react'
import '../index.css'

export const metadata: Metadata = {
  title: 'Aether',
  description: 'Aether developer workbench',
}

export default function RootLayout({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <html lang="en">
      <body className="h-full">
        <div id="root" className="h-full">
          {children}
        </div>
      </body>
    </html>
  )
}
