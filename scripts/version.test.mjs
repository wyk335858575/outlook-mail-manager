import assert from 'node:assert/strict'
import test from 'node:test'

import { assertSequentialRelease, nextVersion, parseVersion } from './version.mjs'

const tag = (value) => ({ tag: `v${value}`, version: parseVersion(value) })

test('version bump uses decimal carry across all three components', () => {
  const cases = [
    ['1.0.0', '1.0.1'],
    ['1.0.9', '1.1.0'],
    ['1.1.9', '1.2.0'],
    ['1.9.9', '2.0.0'],
  ]
  for (const [current, expected] of cases) {
    assert.equal(nextVersion(parseVersion(current)).value, expected)
  }
})

test('sequential release validation rejects skips and unsupported versions', () => {
  assert.doesNotThrow(() => assertSequentialRelease('1.1.0', [tag('1.0.9')]))
  assert.doesNotThrow(() => assertSequentialRelease('2.0.0', [tag('1.9.9')]))
  assert.throws(() => assertSequentialRelease('1.1.0', [tag('1.0.8')]))
  assert.throws(() => assertSequentialRelease('1.10.0', [tag('1.0.9')]))
  assert.throws(() => assertSequentialRelease('1.0.10', [tag('1.0.9')]))
})
