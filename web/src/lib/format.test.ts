import { errorSentence } from '@/lib/format'

describe('errorSentence', () => {
  it('drops the RPC method and Go package names and keeps the sentence', () => {
    expect(errorSentence(new Error('run.mode.switch: scheduler: invalid run state transition: the agent has not reported its session yet')))
      .toBe('invalid run state transition: the agent has not reported its session yet')
    expect(errorSentence(new Error('/api/runs: Not Found'))).toBe('/api/runs: Not Found')
    expect(errorSentence('plain words')).toBe('plain words')
  })
})
