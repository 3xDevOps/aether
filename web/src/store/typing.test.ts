import { beforeEach, describe, expect, it } from 'vitest'
import { useStore } from '@/store'
import { activeTypers, nextTypingExpiry, typingLabel, typingTtlMs } from '@/store/typing'

describe('typingLabel', () => {
  it('names up to three people like Discord and then says several', () => {
    expect(typingLabel([])).toBe('')
    expect(typingLabel(['Alice'])).toBe('Alice is typing')
    expect(typingLabel(['Alice', 'Bob'])).toBe('Alice and Bob are typing')
    expect(typingLabel(['Alice', 'Bob', 'Carol'])).toBe('Alice, Bob and Carol are typing')
    expect(typingLabel(['Alice', 'Bob', 'Carol', 'Dan'])).toBe('Several people are typing')
  })
})

describe('typing entries', () => {
  beforeEach(() => {
    useStore.setState({ typing: {} })
  })

  it('expires an entry a few seconds after the last signal and renews it on the next', () => {
    const { noteTyping } = useStore.getState()
    noteTyping('run_1', 'bob', 1_000)
    expect(activeTypers(useStore.getState().typing.run_1, 1_000 + typingTtlMs - 1, 'me')).toEqual(['bob'])
    expect(activeTypers(useStore.getState().typing.run_1, 1_000 + typingTtlMs, 'me')).toEqual([])
    noteTyping('run_1', 'bob', 4_000)
    expect(activeTypers(useStore.getState().typing.run_1, 1_000 + typingTtlMs, 'me')).toEqual(['bob'])
  })

  it('never lists the local member and keeps the longest typist first', () => {
    const { noteTyping } = useStore.getState()
    noteTyping('run_1', 'zed', 1_000)
    noteTyping('run_1', 'me', 1_500)
    noteTyping('run_1', 'amy', 2_000)
    noteTyping('run_1', 'zed', 3_000)
    expect(activeTypers(useStore.getState().typing.run_1, 3_000, 'me')).toEqual(['zed', 'amy'])
  })

  it('drops an entry when the member stops or sends, leaving other runs alone', () => {
    const { noteTyping, clearTyping } = useStore.getState()
    noteTyping('run_1', 'bob', 1_000)
    noteTyping('run_1', 'amy', 1_000)
    noteTyping('run_2', 'bob', 1_000)
    clearTyping('run_1', 'bob')
    expect(Object.keys(useStore.getState().typing.run_1 ?? {})).toEqual(['amy'])
    clearTyping('run_1')
    expect(useStore.getState().typing.run_1).toBeUndefined()
    expect(Object.keys(useStore.getState().typing.run_2 ?? {})).toEqual(['bob'])
  })

  it('sweeps lapsed entries of every run on the next signal', () => {
    const { noteTyping } = useStore.getState()
    noteTyping('run_1', 'bob', 1_000)
    noteTyping('run_2', 'amy', 1_000 + typingTtlMs + 1)
    expect(Object.keys(useStore.getState().typing)).toEqual(['run_2'])
  })

  it('reports when the next entry lapses', () => {
    const { noteTyping } = useStore.getState()
    noteTyping('run_1', 'bob', 1_000)
    noteTyping('run_1', 'amy', 2_000)
    expect(nextTypingExpiry(useStore.getState().typing.run_1, 1_500)).toBe(1_000 + typingTtlMs)
    expect(nextTypingExpiry(undefined, 0)).toBeUndefined()
  })
})
