import { emptyDetachedStyleSheets } from './setup'

/**
 * By selector text, not by `ownerNode`: jsdom hands back its internal node
 * there, and a detached element is re-registered under a fresh sheet object,
 * so neither identity survives the removal this test is about.
 */
function sheetRuled(selector: string): CSSStyleSheet {
  const sheet = [...document.styleSheets].find((candidate) =>
    [...candidate.cssRules].some((rule) => rule.cssText.includes(selector)),
  )
  if (!sheet) throw new Error(`no stylesheet carries ${selector}`)
  return sheet
}

describe('detached stylesheet cleanup', () => {
  it('empties an orphaned sheet and leaves a live one alone', () => {
    const live = document.createElement('style')
    live.textContent = '.setup-live {}'
    document.head.append(live)

    const wrapper = document.createElement('div')
    const orphan = document.createElement('style')
    orphan.textContent = '.setup-orphan {}'
    wrapper.append(orphan)
    document.body.append(wrapper)
    wrapper.remove()

    // Removing the ancestor leaves the sheet listed, rules and all, and
    // removing the element itself no longer unregisters it. That is the
    // buildup this purge exists to bound.
    orphan.remove()
    const orphanSheet = sheetRuled('.setup-orphan')
    const liveSheet = sheetRuled('.setup-live')
    expect(orphanSheet.ownerNode?.isConnected).toBe(false)
    expect(orphanSheet.cssRules).toHaveLength(1)

    emptyDetachedStyleSheets()

    expect(orphanSheet.cssRules).toHaveLength(0)
    expect(liveSheet.cssRules).toHaveLength(1)
    live.remove()
  })
})
