'use client'
import dynamic from 'next/dynamic'

const Harness = dynamic(() => import('./harness'), { ssr: false })

export default function Page() {
  return <Harness />
}
