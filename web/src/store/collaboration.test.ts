import { describe, expect, it } from 'vitest'
import { createRootStore } from '@/store'
import { evidencePacket, roomMessage } from '@/test/fixtures'
const createdAt = '2026-08-14T10:00:00Z'
const oldestID = '01ARZ3NDEKTSV4RRFFQ69G5FAV'
const middleID = '01ARZ3NDEKTSV4RRFFQ69G5FAW'
const newestID = '01ARZ3NDEKTSV4RRFFQ69G5FAX'

describe('collaboration slice', () => {
  it('orders same-timestamp room messages by ascending ID', () => {
    const store = createRootStore()
    const oldest = roomMessage({ id: oldestID, body: 'oldest', created_at: createdAt })
    const middle = roomMessage({ id: middleID, body: 'middle', created_at: createdAt })
    const newest = roomMessage({ id: newestID, body: 'newest', created_at: createdAt })

    store.getState().setRoomPage('run_1', [newest, middle, oldest], 'before-oldest')

    expect(store.getState().roomMessages.run_1.map((message) => message.id)).toEqual([
      oldestID,
      middleID,
      newestID,
    ])
    expect(store.getState().roomMessages.run_1.map((message) => message.body)).toEqual([
      'oldest',
      'middle',
      'newest',
    ])

    // A replayed page and event update must not duplicate or reorder messages.
    store.getState().setRoomPage('run_1', [oldest, middle, newest], undefined, true)
    store.getState().upsertRoomMessage({ ...middle, body: 'middle updated' })

    expect(store.getState().roomMessages.run_1.map((message) => message.id)).toEqual([
      oldestID,
      middleID,
      newestID,
    ])
    expect(store.getState().roomMessages.run_1).toHaveLength(3)
    expect(store.getState().roomMessages.run_1[1].body).toBe('middle updated')
  })

  it('keeps newer event state when an older list response arrives', () => {
    const store = createRootStore()
    const delivered = roomMessage({
      id: middleID,
      kind: 'steer_request',
      state: 'sent',
      body: 'delivered',
      created_at: createdAt,
      updated_at: '2026-08-14T10:00:02Z',
    })
    store.getState().upsertRoomMessage(delivered)

    store.getState().setRoomPage('run_1', [
      roomMessage({ id: oldestID, body: 'loaded from page', created_at: createdAt }),
      { ...delivered, state: 'queued', body: 'stale', updated_at: '2026-08-14T10:00:01Z' },
    ])

    expect(store.getState().roomMessages.run_1.map((message) => message.id)).toEqual([
      oldestID,
      middleID,
    ])
    expect(store.getState().roomMessages.run_1[1]).toMatchObject({
      state: 'sent',
      body: 'delivered',
    })
  })
  it('orders fractional RFC3339Nano instants numerically', () => {
    const store = createRootStore()
    const older = roomMessage({
      id: newestID,
      body: 'older',
      created_at: '2026-08-14T10:00:00.1Z',
      updated_at: '2026-08-14T10:00:00.1Z',
    })
    const newer = roomMessage({
      id: oldestID,
      body: 'newer',
      created_at: '2026-08-14T10:00:00.11Z',
      updated_at: '2026-08-14T10:00:00.11Z',
    })
    store.getState().setRoomPage('run_1', [newer, older])
    expect(store.getState().roomMessages.run_1.map((message) => message.body)).toEqual(['older', 'newer'])

    store.getState().upsertRoomMessage({ ...older, state: 'queued', body: 'stale' })
    store.getState().upsertRoomMessage({ ...older, state: 'sent', body: 'fresh', updated_at: '2026-08-14T10:00:00.11Z' })
    expect(store.getState().roomMessages.run_1.find((message) => message.id === older.id)?.body).toBe('fresh')
  })

  it('preserves an older-history cursor across newest-page refreshes', () => {
    const store = createRootStore()
    store.getState().setRoomPage('run_1', [roomMessage({ id: newestID })], 'cursor-100')
    store.getState().setRoomPage('run_1', [roomMessage({ id: middleID, created_at: '2026-08-14T10:01:00Z' })], 'cursor-refresh')
    expect(store.getState().roomNextBefore.run_1).toBe('cursor-100')
    store.getState().setRoomPage('run_1', [roomMessage({ id: oldestID })], undefined, true)
    expect(store.getState().roomNextBefore.run_1).toBeUndefined()
  })
  it('distinguishes uninitialized pagination and never revives exhausted history', () => {
    const store = createRootStore()
    store.getState().initializeRoomPagination('run_room')
    expect(store.getState().roomPagination.run_room).toEqual({ initialized: false, exhausted: false })

    store.getState().setRoomPage('run_room', [roomMessage({ id: 'room-old' })], 'room-cursor')
    expect(store.getState().roomPagination.run_room).toEqual({ initialized: true, exhausted: false })
    store.getState().setRoomPage('run_room', [roomMessage({ id: 'room-older' })], undefined, true)
    expect(store.getState().roomPagination.run_room).toEqual({ initialized: true, exhausted: true })
    store.getState().setRoomPage('run_room', [roomMessage({ id: 'room-new' })], 'room-refresh-cursor')
    expect(store.getState().roomNextBefore.run_room).toBeUndefined()
    expect(store.getState().roomPagination.run_room.exhausted).toBe(true)
  })

  it('applies the same no-revival rule to exhausted evidence pagination', () => {
    const store = createRootStore()
    store.getState().initializeEvidencePagination('run_evidence')
    store.getState().setEvidencePage('run_evidence', [evidencePacket({ id: 'packet-old' })], 'evidence-cursor')
    expect(store.getState().evidencePagination.run_evidence).toEqual({ initialized: true, exhausted: false })
    store.getState().setEvidencePage('run_evidence', [evidencePacket({ id: 'packet-older' })], undefined, true)
    expect(store.getState().evidencePagination.run_evidence).toEqual({ initialized: true, exhausted: true })
    store.getState().setEvidencePage('run_evidence', [evidencePacket({ id: 'packet-new' })], 'evidence-refresh-cursor')
    expect(store.getState().evidenceNextBefore.run_evidence).toBeUndefined()
    expect(store.getState().evidencePagination.run_evidence.exhausted).toBe(true)
  })
})
