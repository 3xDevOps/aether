import { isRunLocal, normalizeAddress } from './address'

describe('normalizeAddress', () => {
  it.each([
    ['localhost:3000', 'http://localhost:3000'],
    ['localhost', 'http://localhost'],
    ['127.0.0.1:5173', 'http://127.0.0.1:5173'],
    ['0.0.0.0:8080/app?tab=1', 'http://0.0.0.0:8080/app?tab=1'],
    ['[::1]:3000', 'http://[::1]:3000'],
    ['app.localhost:3000', 'http://app.localhost:3000'],
    ['10.0.0.7:9000', 'http://10.0.0.7:9000'],
    ['example.com', 'https://example.com'],
    ['example.com:8443/path', 'https://example.com:8443/path'],
    ['localhost.example.com', 'https://localhost.example.com'],
  ])('gives %s a scheme', (typed, sent) => {
    expect(normalizeAddress(typed)).toBe(sent)
  })

  it.each(['http://localhost:3000/', 'HTTPS://example.com/a b', 'about:blank', 'file:///etc/hosts'])('leaves %s alone', (typed) => {
    expect(normalizeAddress(typed)).toBe(typed)
  })

  it('trims, and sends nothing for nothing', () => {
    expect(normalizeAddress('  localhost:3000 ')).toBe('http://localhost:3000')
    expect(normalizeAddress('   ')).toBe('')
  })
})

describe('isRunLocal', () => {
  it.each(['http://localhost:3000', 'https://localhost/', 'http://127.0.0.1:5173/path', 'http://0.0.0.0:8080', 'http://[::1]:3000/'])('takes %s', (uri) => {
    expect(isRunLocal(uri)).toBe(true)
  })

  it.each(['https://example.com', 'http://localhost.example.com', 'http://192.168.1.4:3000', 'ftp://localhost/file', 'localhost:3000', 'not a url'])('leaves %s', (uri) => {
    expect(isRunLocal(uri)).toBe(false)
  })
})
