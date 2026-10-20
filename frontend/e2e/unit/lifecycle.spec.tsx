import { expect, test } from '@playwright/test'
import { actionLabel } from '../../src/lib/auditLabels'

// Pure checks: no browser, no network, no clock.

test('every lifecycle action has a readable audit label', () => {
  for (const a of [
    'ownership.offer', 'ownership.accept', 'ownership.cancel', 'ownership.decline', 'ownership.expire',
    'workspace.delete_scheduled', 'workspace.restored', 'audit.export', 'experiment.control_change',
  ]) {
    expect(actionLabel(a), a).not.toBe(a)
  }
})
