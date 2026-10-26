import { launchParams, missingNames, placeholderNames, renderTask, segments, tokenEstimate } from '@/components/templates/placeholders'
import { template } from '@/test/fixtures'

const task = 'Audit {{ecosystem}} for {{ severity }}; {{severity}} first. Leave {{1bad}} and {{ x y }} alone.'
const audit = { ...template, task, params: { ecosystem: 'npm', severity: '' } }

describe('template placeholders', () => {
  it('finds the names the server substitutes, once each, in order', () => {
    expect(placeholderNames(task)).toEqual(['ecosystem', 'severity'])
    expect(placeholderNames('{{\u00a0area}} {{\varea}}')).toEqual([])
    expect(segments('{{a}} and {{a}}.')).toEqual([{ name: 'a', first: true }, { text: ' and ' }, { name: 'a', first: false }, { text: '.' }])
  })

  it('renders typed values over defaults, keeps an empty default, and leaves an unfilled placeholder as written', () => {
    expect(renderTask(audit, { ecosystem: 'go' })).toBe('Audit go for ;  first. Leave {{1bad}} and {{ x y }} alone.')
    const bare = { ...template, task: 'Fix {{pkg}}.' }
    expect(renderTask(bare, {})).toBe('Fix {{pkg}}.')
    expect(missingNames(bare, {})).toEqual(['pkg'])
    expect(missingNames(audit, {})).toEqual([])
  })

  it('sends only typed values that differ from the default', () => {
    expect(launchParams(audit, { ecosystem: 'npm', severity: '' })).toBeUndefined()
    expect(launchParams(audit, { ecosystem: 'pip', severity: 'high', stray: 'x' })).toEqual({ ecosystem: 'pip', severity: 'high' })
  })

  it('reads only values set on the template or typed, even for a name Object.prototype carries', () => {
    const named = { ...template, task: 'Call {{constructor}} via {{toString}}.', params: { toString: 'str' } }
    expect(missingNames(named, {})).toEqual(['constructor'])
    expect(renderTask(named, {})).toBe('Call {{constructor}} via str.')
    expect(launchParams(named, {})).toBeUndefined()
  })

  it('estimates about four characters per token', () => {
    expect(tokenEstimate('x'.repeat(40))).toBe(10)
  })
})
