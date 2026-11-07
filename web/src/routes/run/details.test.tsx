import { render, screen } from '@testing-library/react'
import { DoneNotes } from '@/routes/run/details'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { roomMessage, run } from '@/test/fixtures'
import type { RoomMessage } from '@/lib/types'

const record = toRecord(run())

function seed(messages: RoomMessage[]) {
  useStore.setState({ roomMessages: { [record.id]: messages } })
}

describe('done history', () => {
  it('sorts by when a note was completed, newest first, not when it was posted', () => {
    seed([
      roomMessage({
        id: 'old-note', kind: 'comment', resolution: 'done', body: 'old note, just completed',
        created_at: '2026-08-14T08:00:00Z', resolved_at: '2026-08-14T12:00:00Z',
      }),
      roomMessage({
        id: 'new-note', kind: 'comment', resolution: 'done', body: 'new note, completed earlier',
        created_at: '2026-08-14T11:00:00Z', resolved_at: '2026-08-14T10:00:00Z',
      }),
    ])
    render(<DoneNotes run={record} inset={false} />)
    const bodies = screen.getAllByText(/completed/)
    expect(bodies.map((el) => el.textContent)).toEqual(['old note, just completed', 'new note, completed earlier'])
  })
})
