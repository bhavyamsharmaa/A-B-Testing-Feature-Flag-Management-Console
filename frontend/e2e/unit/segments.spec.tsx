import { expect, test } from '@playwright/test'
import {
  MAX_KEY_LENGTH,
  MAX_MEMBERS,
  MAX_PASTE_CHARS,
  describeMerge,
  filterMembers,
  mergeMembers,
  parseMemberText,
} from '../../src/lib/segmentMembers'
import {
  canNegate,
  canon,
  changeOperator,
  clausesFromSaved,
  clausesToPayload,
  fromSaved,
  hasErrors,
  isRfc3339,
  isSemver,
  newClause,
  parseServerRuleError,
  parseServerSegmentRuleError,
  toPayload,
  validate,
  validateClause,
  valueKind,
  type DraftClause,
  type Operator,
} from '../../src/lib/targeting'
import { actionLabel } from '../../src/lib/auditLabels'
import type { TargetingRule } from '../../src/types'

// Pure checks: no browser, no network.

const keys = (n: number, prefix = 'u') => Array.from({ length: n }, (_, i) => `${prefix}${i}`)

test.describe('pasting member keys', () => {
  test('splits on lines, commas and tabs; trims; drops blanks and repeats', () => {
    const p = parseMemberText('  user-1 \r\nuser-2\n\n\nuser-2, user-3\tuser-4,,\n user-1')
    expect(p.keys).toEqual(['user-1', 'user-2', 'user-3', 'user-4'])
    expect(p.duplicates).toBe(2)
    expect(p.invalidCount).toBe(0)
  })

  test('keys are exact: case and inner spaces are kept', () => {
    expect(parseMemberText('Alice\nalice\nA B').keys).toEqual(['Alice', 'alice', 'A B'])
  })

  test('unicode keys count characters, not bytes', () => {
    const ok = 'é'.repeat(MAX_KEY_LENGTH)
    expect(parseMemberText(ok).keys).toEqual([ok])
    expect(parseMemberText('é'.repeat(MAX_KEY_LENGTH + 1)).invalidCount).toBe(1)
    expect(parseMemberText('😀'.repeat(MAX_KEY_LENGTH)).keys).toHaveLength(1)
  })

  test('over-long and control-character keys are reported by name, a few at a time', () => {
    const p = parseMemberText([...keys(8, 'x').map(() => 'y'.repeat(MAX_KEY_LENGTH + 1)), 'bad\u0000key', 'fine'].join('\n'))
    expect(p.keys).toEqual(['fine'])
    expect(p.invalidCount).toBe(9)
    expect(p.invalid).toHaveLength(5)
    expect(p.invalid[0].reason).toContain('longer than 256')
    expect(p.invalid[0].value.length).toBeLessThanOrEqual(25) // previewed, never echoed in full
  })

  test('a paste over the size cap is refused unread', () => {
    const p = parseMemberText('a\n'.repeat(MAX_PASTE_CHARS))
    expect(p.tooLarge).toBe(true)
    expect(p.keys).toHaveLength(0)
    expect(mergeMembers([], p).error).toContain('too large')
  })

  test('a full-size paste parses quickly', () => {
    const text = keys(MAX_MEMBERS, 'user-').join('\n')
    const t0 = Date.now()
    const p = parseMemberText(text)
    expect(p.keys).toHaveLength(MAX_MEMBERS)
    expect(Date.now() - t0).toBeLessThan(2000)
  })
})

test.describe('merging into the member list', () => {
  test('adds new keys after the existing ones and counts what it skipped', () => {
    const parsed = parseMemberText('a\nb\nc\nc')
    const r = mergeMembers(['b', 'z'], parsed)
    expect(r.error).toBeNull()
    expect(r.members).toEqual(['b', 'z', 'a', 'c'])
    expect(r.added).toBe(2)
    expect(r.alreadyMembers).toBe(1)
    expect(describeMerge(r, parsed)).toBe('Added 2 members. 2 duplicates ignored.')
  })

  test('exactly the limit is allowed, one more is refused whole', () => {
    expect(mergeMembers(keys(MAX_MEMBERS - 1), parseMemberText('last')).members).toHaveLength(MAX_MEMBERS)
    const current = keys(MAX_MEMBERS)
    const r = mergeMembers(current, parseMemberText('one-more\nanother'))
    expect(r.error).toMatch(/at most 10,000/)
    expect(r.error).toMatch(/Nothing was added/)
    expect(r.members).toBe(current)
    expect(r.added).toBe(0)
  })

  test('repeating existing members at the limit is fine: they add nothing', () => {
    const current = keys(MAX_MEMBERS)
    expect(mergeMembers(current, parseMemberText('u1\nu2')).error).toBeNull()
  })

  test('an invalid entry refuses the whole paste', () => {
    const r = mergeMembers(['a'], parseMemberText('good\n' + 'x'.repeat(300)))
    expect(r.error).toMatch(/1 entry could not be used, so nothing was added/)
    expect(r.members).toEqual(['a'])
  })

  test('an empty paste says so', () => {
    expect(mergeMembers([], parseMemberText(' \n , \n')).error).toBe('No keys found in what you pasted.')
  })

  test('the filter is case-insensitive and bounded', () => {
    const f = filterMembers(['Alice', 'alicia', 'Bob', ...keys(500, 'zed')], 'ALIC', 100)
    expect(f.shown).toEqual(['Alice', 'alicia'])
    expect(f.matches).toBe(2)
    const all = filterMembers(keys(500), '', 100)
    expect(all.shown).toHaveLength(100)
    expect(all.matches).toBe(500)
  })
})

const clauseOf = (patch: Partial<DraftClause>): DraftClause => ({ ...newClause(), attribute: 'a', ...patch })

test.describe('targeting operators in the builder', () => {
  test('each operator takes the kind of value it should', () => {
    const kinds: Record<Operator, string> = {
      equals: 'scalar', in: 'list', contains: 'scalar', greaterThan: 'number', exists: 'none', startsWith: 'text', endsWith: 'text',
      lessThan: 'number', lessThanOrEqual: 'number', greaterThanOrEqual: 'number', semverEquals: 'semver', semverLessThan: 'semver',
      semverLessThanOrEqual: 'semver', semverGreaterThan: 'semver', semverGreaterThanOrEqual: 'semver', dateBefore: 'date', dateAfter: 'date',
      dateEquals: 'date', matchesRegex: 'regex', inSegment: 'segment', notInSegment: 'segment',
    }
    for (const [op, kind] of Object.entries(kinds)) expect(valueKind(op as Operator), op).toBe(kind)
  })

  test('negation is offered for everything but exists and the segment operators', () => {
    expect(canNegate('equals')).toBe(true)
    expect(canNegate('matchesRegex')).toBe(true)
    expect(canNegate('exists')).toBe(false)
    expect(canNegate('inSegment')).toBe(false)
    expect(canNegate('notInSegment')).toBe(false)
  })

  test('payloads match what the API takes', () => {
    const rules = toPayload([
      {
        id: 'r', variationId: 'on', locked: null,
        clauses: [
          clauseOf({ operator: 'startsWith', text: ' ab ', negate: true }),
          clauseOf({ operator: 'lessThanOrEqual', text: '5' }),
          clauseOf({ operator: 'semverGreaterThanOrEqual', text: '1.2.3' }),
          clauseOf({ operator: 'dateBefore', text: '2024-05-01T00:00:00Z' }),
          clauseOf({ operator: 'matchesRegex', text: ' ^a b$ ' }),
          clauseOf({ operator: 'equals', valueType: 'number', text: '5', negate: true }),
          { ...newClause(), attribute: 'ignored', operator: 'inSegment', text: ' beta ' },
          clauseOf({ operator: 'exists', negate: true }), // cannot be negated: the flag is dropped
        ],
      },
    ])
    expect(rules[0].clauses).toEqual([
      { attribute: 'a', operator: 'startsWith', value: 'ab', negate: true },
      { attribute: 'a', operator: 'lessThanOrEqual', value: 5 },
      { attribute: 'a', operator: 'semverGreaterThanOrEqual', value: '1.2.3' },
      { attribute: 'a', operator: 'dateBefore', value: '2024-05-01T00:00:00Z' },
      { attribute: 'a', operator: 'matchesRegex', value: ' ^a b$ ' }, // patterns are exact
      { attribute: 'a', operator: 'equals', value: 5, negate: true },
      { attribute: '', operator: 'inSegment', value: 'beta' },
      { attribute: 'a', operator: 'exists' },
    ])
  })

  test('saved rules round-trip, including negation and segments; unknown shapes stay locked', () => {
    const saved: TargetingRule[] = [
      {
        variationId: 'on',
        clauses: [
          { attribute: 'email', operator: 'endsWith', value: '@x.dev', negate: true },
          { attribute: '', operator: 'notInSegment', value: 'beta' },
          { attribute: 'v', operator: 'semverEquals', value: '1.0.0' },
          { attribute: 'n', operator: 'greaterThanOrEqual', value: 3 },
        ],
      },
      { variationId: 'on', clauses: [{ attribute: 'a', operator: 'exists', negate: true }] }, // the server would not have stored it
      { variationId: 'on', clauses: [{ attribute: 'a', operator: 'equals', value: 'x', extra: 1 } as never] },
      { variationId: 'on', clauses: [{ attribute: 'a', operator: 'someFutureOperator', value: 'x' }] },
    ]
    const drafts = fromSaved(saved)
    expect(drafts[0].locked).toBeNull()
    expect(drafts[1].locked).not.toBeNull()
    expect(drafts[2].locked).not.toBeNull()
    expect(drafts[3].locked).not.toBeNull()
    const back = toPayload(drafts)
    expect(canon(back)).toBe(canon(saved))
    expect(back[1]).toBe(saved[1]) // locked rules go back untouched
  })

  test('changing the operator keeps what carries over', () => {
    const eq = clauseOf({ operator: 'equals', text: 'pro' })
    expect(changeOperator(eq, 'startsWith')).toMatchObject({ operator: 'startsWith', text: 'pro', negate: false })
    expect(changeOperator({ ...eq, negate: true }, 'matchesRegex')).toMatchObject({ negate: true, text: 'pro' })
    expect(changeOperator({ ...eq, negate: true }, 'exists')).toMatchObject({ negate: false, text: '' })
    expect(changeOperator(eq, 'lessThan').text).toBe('') // "pro" is not a number
    expect(changeOperator(clauseOf({ operator: 'equals', text: '5' }), 'lessThan').text).toBe('5')
    const seg = changeOperator({ ...eq, negate: true }, 'inSegment')
    expect(seg).toMatchObject({ operator: 'inSegment', attribute: '', negate: false, text: '' })
    expect(changeOperator({ ...seg, text: 'beta' }, 'notInSegment').text).toBe('beta')
    expect(changeOperator({ ...seg, text: 'beta' }, 'equals').text).toBe('')
  })

  test('validation explains what is wrong, per operator', () => {
    const msg = (c: Partial<DraftClause>, known: string[] | null = null) => validateClause(clauseOf(c), known ? new Set(known) : null)
    expect(msg({ operator: 'startsWith', text: '' })).toBe('Value is required.')
    expect(msg({ operator: 'lessThan', text: 'x' })).toBe('Enter a number.')
    expect(msg({ operator: 'semverEquals', text: 'v1.2.3' })).toMatch(/no leading "v"/)
    expect(msg({ operator: 'semverEquals', text: '1.2.3-rc.1+b5' })).toBeNull()
    expect(msg({ operator: 'dateBefore', text: '2024-05-01' })).toMatch(/RFC 3339/)
    expect(msg({ operator: 'dateBefore', text: '2024-05-01T10:00:00+02:00' })).toBeNull()
    expect(msg({ operator: 'matchesRegex', text: '' })).toBe('Enter a regular expression.')
    expect(msg({ operator: 'matchesRegex', text: 'a'.repeat(257) })).toMatch(/longer than 256/)
    expect(msg({ operator: 'matchesRegex', text: '^a+$' })).toBeNull()
    expect(msg({ operator: 'inSegment', text: '' })).toBe('Choose a segment.')
    expect(msg({ operator: 'inSegment', text: 'gone' }, ['beta'])).toMatch(/does not exist/)
    expect(msg({ operator: 'inSegment', text: 'beta' }, ['beta'])).toBeNull()
    expect(msg({ operator: 'inSegment', text: 'whatever' }, null)).toBeNull() // list unknown: the server decides
    expect(validateClause({ ...clauseOf({ operator: 'inSegment', text: 'beta' }), attribute: '' })).toBeNull() // no attribute needed
    expect(msg({ attribute: '', operator: 'startsWith', text: 'x' })).toBe('Attribute is required.')
    const errors = validate([{ id: 'r', variationId: 'on', locked: null, clauses: [clauseOf({ operator: 'semverEquals', text: '1' })] }], new Set(['on']))
    expect(hasErrors(errors)).toBe(true)
  })

  test('semver and RFC 3339 checks', () => {
    for (const ok of ['0.0.0', '1.2.3', '10.20.30', '1.0.0-alpha.1', '1.0.0-0.3.7', '1.0.0+20130313144700', '1.0.0-beta+exp.sha.5114f85']) expect(isSemver(ok), ok).toBe(true)
    for (const bad of ['', '1', '1.2', '1.2.3.4', 'v1.2.3', '01.2.3', '1.2.3-', '1.2.3-01', '1.2.3 ', 'a.b.c']) expect(isSemver(bad), bad).toBe(false)
    for (const ok of ['2024-05-01T00:00:00Z', '2024-05-01T12:30:45.123+05:30', '2024-12-31T23:59:59-08:00']) expect(isRfc3339(ok), ok).toBe(true)
    for (const bad of ['', '2024-05-01', '2024-05-01 00:00:00Z', '2024-05-01T00:00:00', '2024-13-01T00:00:00Z', 'May 1 2024', '2024-05-01T00:00:00+0200']) expect(isRfc3339(bad), bad).toBe(false)
  })

  test('segment rule helpers', () => {
    const saved = [{ attribute: 'plan', operator: 'equals', value: 'pro' }, { attribute: 'v', operator: 'semverGreaterThan', value: '2.0.0', negate: true }]
    const drafts = clausesFromSaved(saved)!
    expect(clausesToPayload(drafts)).toEqual(saved)
    expect(clausesFromSaved([{ attribute: 'a', operator: 'nope', value: 1 }])).toBeNull()
    expect(parseServerSegmentRuleError('rules[2]: inSegment cannot be used')).toEqual({ clause: 2, text: 'inSegment cannot be used' })
    expect(parseServerSegmentRuleError('name must be 1-120 characters')).toBeNull()
    expect(parseServerRuleError('targetingRules[1]: clause 0: segment "x" does not exist in this workspace')).toEqual({
      rule: 1, clause: 0, text: 'segment "x" does not exist in this workspace',
    })
  })

  test('segment changes have audit labels', () => {
    expect(actionLabel('segment.create')).toBe('Segment created')
    expect(actionLabel('segment.force_delete')).toBe('Segment force-deleted')
  })
})
