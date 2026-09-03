import {
  allowed,
  STEER_OTHERS_ADMINS_ONLY,
  type Actor,
  type RunPermission,
} from '@/lib/permissions'
import type { Member } from '@/lib/types'

// The oracle is the design-doc role table, written out again rather than
// derived from the implementation - the same shape as `want` in
// internal/permissions/permissions_test.go. The two matrices below are the
// tripwire for the mirror drifting away from the Go policy it copies.
function want(
  permission: RunPermission,
  role: Member['role'],
  owner: boolean,
  isProtected: boolean,
  steerOthers: string,
): boolean {
  if (role === 'admin') return true
  switch (permission) {
    case 'launch':
      return role === 'collaborator'
    case 'handoff':
    case 'protect':
      return owner
    case 'steer':
    case 'kill':
      if (role !== 'collaborator') return false
      if (owner) return true
      return !isProtected && steerOthers !== STEER_OTHERS_ADMINS_ONLY
  }
}

const roles: Member['role'][] = ['viewer', 'collaborator', 'admin']
const permissions: RunPermission[] = ['steer', 'kill', 'launch', 'handoff', 'protect']
const settings = ['', STEER_OTHERS_ADMINS_ONLY]
const actor = 'mem_actor'
const other = 'mem_other'

describe('the permission mirror answers as internal/permissions does', () => {
  for (const role of roles) {
    for (const permission of permissions) {
      for (const owns of [true, false]) {
        for (const isProtected of [false, true]) {
          for (const steerOthers of settings) {
            const name = `${role}/${permission}/owns=${owns}/protected=${isProtected}/steer_others="${steerOthers}"`
            it(name, () => {
              expect(
                allowed(
                  permission,
                  { id: actor, role },
                  {
                    owner: owns ? actor : other,
                    protected: isProtected,
                    steerOthers,
                  },
                ),
              ).toBe(want(permission, role, owns, isProtected, steerOthers))
            })
          }
        }
      }
    }
  }
})

// The named rules the matrix derives, mirroring TestCheckSpotRules.
test.each<[string, RunPermission, Actor, Parameters<typeof allowed>[2], boolean]>([
  ['viewer cannot steer own run', 'steer', { id: actor, role: 'viewer' }, { owner: actor }, false],
  ['viewer cannot launch', 'launch', { id: actor, role: 'viewer' }, {}, false],
  ['collaborator launches', 'launch', { id: actor, role: 'collaborator' }, {}, true],
  [
    'collaborator kills another run by default',
    'kill',
    { id: actor, role: 'collaborator' },
    { owner: other },
    true,
  ],
  [
    'admins_only blocks a collaborator steering another run',
    'steer',
    { id: actor, role: 'collaborator' },
    { owner: other, steerOthers: STEER_OTHERS_ADMINS_ONLY },
    false,
  ],
  [
    'admins_only keeps own runs steerable',
    'steer',
    { id: actor, role: 'collaborator' },
    { owner: actor, steerOthers: STEER_OTHERS_ADMINS_ONLY },
    true,
  ],
  [
    'a protected run blocks a non-owner kill even by default',
    'kill',
    { id: actor, role: 'collaborator' },
    { owner: other, protected: true },
    false,
  ],
  [
    'a protected run stays open to its owner',
    'steer',
    { id: actor, role: 'collaborator' },
    { owner: actor, protected: true },
    true,
  ],
  [
    'an admin bypasses protected and admins_only',
    'kill',
    { id: actor, role: 'admin' },
    { owner: other, protected: true, steerOthers: STEER_OTHERS_ADMINS_ONLY },
    true,
  ],
  [
    'handoff by a non-owner collaborator is denied',
    'handoff',
    { id: actor, role: 'collaborator' },
    { owner: other },
    false,
  ],
  [
    'protect by the owner is allowed',
    'protect',
    { id: actor, role: 'collaborator' },
    { owner: actor },
    true,
  ],
])('%s', (_name, permission, who, target, expected) => {
  expect(allowed(permission, who, target)).toBe(expected)
})

// The one question the Go policy never has to answer: the dashboard renders
// before server.info arrives, so an unknown role is optimistic and the server
// refuses anything this member may not do.
test('an unknown role is allowed everything, because the server still checks', () => {
  for (const permission of permissions) {
    expect(allowed(permission, { id: null, role: null }, { owner: other })).toBe(true)
  }
})

// An empty actor id never counts as ownership, so an unidentified caller
// cannot inherit a run whose owner field is also empty.
test('an empty actor id owns nothing', () => {
  expect(allowed('handoff', { id: '', role: 'collaborator' }, { owner: '' })).toBe(false)
})
