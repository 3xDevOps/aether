import { readFile } from 'node:fs/promises'
import { render, screen } from '@testing-library/react'
import { createRef } from 'react'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { openingTags, sourceFiles } from '@/test/sources'

test.each([
  ['Input', <Input aria-label="Task" />],
  ['Textarea', <Textarea aria-label="Task" />],
])('%s draws the shared focus outline', (_, element) => {
  render(element)

  const control = screen.getByLabelText('Task')
  expect(control.className).toContain('focus-visible:outline-2')
  expect(control.className).toContain('focus-visible:outline-ring')
})

test('Label names the control it wraps, with no id on either', () => {
  render(
    <Label>
      Task
      <Input />
    </Label>,
  )

  expect(screen.getByLabelText('Task').tagName).toBe('INPUT')
})

// Upstream's label is a flex row in a heavier weight, with modifiers that
// answer a disabled peer. The house one wraps its control instead, so it has
// no peer and cannot afford the row: an overwrite from the registry would
// reweight every caption and turn the `flex-1` ones into rows.
test('Label carries the house type scale and nothing else', () => {
  render(<Label>Task</Label>)

  expect(screen.getByText('Task').className).toBe('text-sm')
})

// Four call sites focus or select a field through a ref. Were forwarding to
// break, every one of them would become a silent no-op.
test('a ref reaches the field itself', () => {
  const ref = createRef<HTMLInputElement>()

  render(<Input ref={ref} />)

  expect(ref.current?.tagName).toBe('INPUT')
})

test('no file outside components/ui draws a field by hand', async () => {
  const sources = await sourceFiles()
  expect(sources.length).toBeGreaterThan(50)

  const offenders: string[] = []
  for (const path of sources) {
    if (path.includes('/components/ui/')) continue
    const tags = openingTags(await readFile(path, 'utf8'), ['input', 'textarea', 'label'])
    for (const tag of tags) {
      // A checkbox draws none of this: no border, no padding, no text scale.
      // It is a control of its own kind rather than a field these replace.
      if (tag.name === 'input' && /\btype=["'{]+checkbox/.test(tag.attributes)) continue
      offenders.push(`${path}: <${tag.name}>`)
    }
  }

  expect(offenders).toEqual([])
})
