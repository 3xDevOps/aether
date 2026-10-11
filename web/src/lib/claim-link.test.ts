import { parseClaim } from '@/lib/claim-link'

const id = 'abcdefghijklmnopqrstuvwxyz'

describe('parseClaim', () => {
  it('reads the code and the edge from either claim link', () => {
    expect(parseClaim(` https://edge.example.test/claim/${id}#secret `)).toEqual({
      code: `${id}-secret`,
      edge: 'https://edge.example.test',
    })
    expect(parseClaim(`aether://claim?edge=https%3A%2F%2Fedge.example.test%3A8443&server=${id}#secret`)).toEqual({
      code: `${id}-secret`,
      edge: 'https://edge.example.test:8443',
    })
  })

  it('returns anything else as the code', () => {
    for (const text of [
      `${id}-secret`,
      `https://edge.example.test/claim/${id}`,
      `https://edge.example.test/join/${id}/inv#secret`,
      `aether://claim?server=${id}#secret`,
      `http://edge.example.test/claim/${id}#secret`,
    ]) {
      expect(parseClaim(text)).toEqual({ code: text })
    }
  })
})
