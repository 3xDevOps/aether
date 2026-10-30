import { beforeEach, describe, expect, it } from 'vitest'
import type { DevArtifact, DevBrowserElement } from '@/lib/types'
import { addNote, closeNote, editNote, feedback, feedbackMessage, patchFeedback, selectLogs, summary, updateNote, written, type ElementNote, type LogEntry, type Note, type ScreenshotNote } from './feedback'

const page = { url: 'http://localhost:3000/settings', width: 1280, height: 800 }
const save: DevBrowserElement = {
  tag: 'button',
  role: 'button',
  name: 'Save changes',
  text: 'Save',
  selector: 'button#save',
  attributes: { id: 'save', class: 'btn primary' },
  bounds: { x: 40.4, y: 30, width: 120, height: 36 },
  scroll_x: 0,
  scroll_y: 0,
  html: '<button id="save" class="btn primary">Save ```</button>',
  styles: { display: 'block', color: 'rgb(255, 255, 255)' },
  component: 'App > Toolbar > SaveButton',
  source: 'src/components/SaveButton.tsx:12:5',
}

function elementNote(patch: Partial<ElementNote> = {}): ElementNote {
  return { kind: 'element', id: 'one', page, body: '', at: { page_id: 'page', scroll_x: 0, scroll_y: 0 }, point: { x: 45, y: 34 }, ...patch }
}
function screenshotNote(patch: Partial<ScreenshotNote> = {}): ScreenshotNote {
  return { kind: 'screenshot', id: 'shot', page, body: '', artifact: { path: '/aether/captures/shot.png' } as DevArtifact, ...patch }
}
const consoleRow: LogEntry = { kind: 'console', sequence: 3, time: '2026-10-11T12:01:03.250Z', level: 'error', text: 'Uncaught TypeError: x is not a function', page: page.url }
const failedRow: LogEntry = { kind: 'network', sequence: 4, time: '2026-10-11T12:01:04Z', method: 'GET', url: 'http://localhost:3000/api/user', status: 500, page: page.url }
const lostRow: LogEntry = { kind: 'network', sequence: 5, time: '2026-10-11T12:01:05Z', method: 'POST', url: 'http://localhost:3000/api/save', failure: 'net::ERR_CONNECTION_REFUSED', page: page.url }

beforeEach(() => patchFeedback('run', { notes: [], logs: [], sent: undefined, sending: undefined }))

describe('feedback notes', () => {
  it('opens one card at a time and keeps what the others hold', () => {
    addNote('run', elementNote({ id: 'empty' }))
    addNote('run', elementNote({ id: 'typed' }))
    expect(feedback('run').notes.map((note) => note.id)).toEqual(['typed'])
    updateNote('run', 'typed', { draft: ' Too wide ', element: save })
    addNote('run', screenshotNote())
    expect(feedback('run').notes).toMatchObject([{ id: 'typed', body: 'Too wide', draft: undefined }, { id: 'shot', draft: '' }])
    editNote('run', 'typed')
    expect(feedback('run').notes).toMatchObject([{ id: 'typed', draft: 'Too wide' }, { id: 'shot', body: '', draft: undefined }])
  })

  it('closes a card on its text, drops an element note left empty and keeps a screenshot without words', () => {
    addNote('run', elementNote())
    closeNote('run', 'one', false)
    expect(feedback('run').notes).toEqual([])
    addNote('run', elementNote({ element: save }))
    updateNote('run', 'one', { draft: 'Too wide' })
    closeNote('run', 'one', true)
    editNote('run', 'one')
    updateNote('run', 'one', { draft: 'Reworded' })
    closeNote('run', 'one', false)
    expect(feedback('run').notes).toMatchObject([{ body: 'Too wide', draft: undefined }])
    addNote('run', screenshotNote())
    closeNote('run', 'shot', true)
    expect(feedback('run').notes).toMatchObject([{ id: 'one' }, { id: 'shot', body: '', draft: undefined }])
  })

  it('carries a typed draft, every screenshot, and no element the page has not described', () => {
    const notes: Note[] = [
      elementNote({ id: 'unread', draft: 'Words without an element' }),
      elementNote({ id: 'silent', element: save }),
      elementNote({ id: 'typed', element: save, draft: 'Too wide' }),
      screenshotNote(),
    ]
    expect(written(notes)).toMatchObject([{ id: 'typed', body: 'Too wide', draft: undefined }, { id: 'shot' }])
  })

  it('ticks rows in the order the page logged them', () => {
    selectLogs('run', [failedRow], true)
    selectLogs('run', [consoleRow, lostRow], true)
    expect(feedback('run').logs.map((entry) => entry.sequence)).toEqual([3, 4, 5])
    selectLogs('run', [failedRow], false)
    expect(summary([], feedback('run').logs)).toBe('1 console message, 1 failed request')
  })
})

describe('feedbackMessage', () => {
  it('says what the person wrote first, then what the page says about the element', () => {
    const notes = written([
      elementNote({ element: save, body: 'The label is cut off.', path: '/home/ada/.aether/terminal-images/a.png' }),
      screenshotNote({ body: 'Compare the header with the mock.' }),
    ])
    expect(feedbackMessage(notes, [consoleRow, failedRow, lostRow])).toBe([
      "Feedback from the page in this session's browser: 1 annotation, 1 screenshot, 1 console message, 2 failed requests.",
      'Each screenshot path is an image file inside the session; open it to see what I saw.',
      'Page: http://localhost:3000/settings (viewport 1280x800)',
      '',
      '1. button "Save changes"',
      'The label is cut off.',
      '- Screenshot: /home/ada/.aether/terminal-images/a.png',
      '- Selector: button#save',
      '- Source: src/components/SaveButton.tsx:12:5',
      '- Component: App > Toolbar > SaveButton',
      '- Role: button',
      '- Name: Save changes',
      '- Text: Save',
      '- Attributes: id="save" class="btn primary"',
      '- Bounds: x 40, y 30, 120 x 36',
      '- Styles: display: block; color: rgb(255, 255, 255)',
      '````html',
      '<button id="save" class="btn primary">Save ```</button>',
      '````',
      '',
      '2. Screenshot of the page',
      'Compare the header with the mock.',
      '- Screenshot: /aether/captures/shot.png',
      '',
      'Console (http://localhost:3000/settings):',
      '- 12:01:03Z error Uncaught TypeError: x is not a function',
      '',
      'Failed requests (http://localhost:3000/settings):',
      '- 12:01:04Z GET http://localhost:3000/api/user 500',
      '- 12:01:05Z POST http://localhost:3000/api/save failed: net::ERR_CONNECTION_REFUSED',
    ].join('\n'))
  })

  it('names the page on each note when they are from different pages, and leaves out what is absent', () => {
    const bare: DevBrowserElement = { tag: 'div', selector: 'div.card', bounds: { x: 0, y: 0, width: 10, height: 10 }, scroll_x: 0, scroll_y: 0 }
    const notes = written([
      elementNote({ element: bare, body: 'Too much padding.' }),
      screenshotNote({ page: { ...page, url: 'http://localhost:3000/' } }),
    ])
    expect(feedbackMessage(notes, [])).toBe([
      "Feedback from the page in this session's browser: 1 annotation, 1 screenshot.",
      'Each screenshot path is an image file inside the session; open it to see what I saw.',
      '',
      '1. div',
      'Too much padding.',
      '- Page: http://localhost:3000/settings (viewport 1280x800)',
      '- Selector: div.card',
      '- Bounds: x 0, y 0, 10 x 10',
      '',
      '2. Screenshot of the page',
      '- Page: http://localhost:3000/ (viewport 1280x800)',
      '- Screenshot: /aether/captures/shot.png',
    ].join('\n'))
  })
})
