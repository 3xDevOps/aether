import { parseClaim, parseJoin } from '@/lib/claim-link'

const id = 'abcdefghijklmnopqrstuvwxyz'

describe('parseClaim', () => {
  it('reads the code and the edge from a claim link', () => {
    expect(parseClaim(`aether://claim?edge=https%3A%2F%2Fedge.example.test%3A8443&server=${id}#secret`)).toEqual({
      code: `${id}-secret`,
      edge: 'https://edge.example.test:8443',
    })
  })

  it('returns anything else as the code', () => {
    for (const text of [
      `${id}-secret`,
      `https://edge.example.test/claim/${id}#secret`,
      `https://edge.example.test/join/${id}/inv#secret`,
      `aether://claim?server=${id}#secret`,
      `http://edge.example.test/claim/${id}#secret`,
    ]) {
      expect(parseClaim(text)).toEqual({ code: text })
    }
  })
})

describe('parseJoin', () => {
  it('reads the edge and ids from either join link, and nothing else', () => {
    const named = { edge: 'https://edge.example.test', server: id, invitation: 'inv-7k2m9q4xbd' }
    expect(parseJoin(` https://edge.example.test/join/${id}/inv-7k2m9q4xbd#secret `)).toEqual(named)
    expect(parseJoin(`aether://join?edge=https%3A%2F%2Fedge.example.test&server=${id}&invitation=inv-7k2m9q4xbd#secret`)).toEqual(named)
    expect(parseJoin(`https://Edge.example.test:443/join/${id}/inv-7k2m9q4xbd#secret`)).toEqual({ ...named, edge: 'https://edge.example.test:443' })
    for (const text of [
      `https://edge.example.test/join/${id}/inv-7k2m9q4xbd`,
      `aether://join?edge=https%3A%2F%2Fedge.example.test&server=${id}#secret`,
      `aether://join?edge=https%3A%2F%2Fuser%40edge.example.test%2Fx&server=${id}&invitation=inv-7k2m9q4xbd#secret`,
      `https://edge.example.test/claim/${id}#secret`,
      `${id}-secret`,
    ]) {
      expect(parseJoin(text)).toBeNull()
    }
  })
})
