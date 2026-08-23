import { describe, expect, it, vi } from 'vitest'

import { transitionToSection } from './App'

describe('directory navigation', () => {
  it('keeps the current page visible until the target section is ready', () => {
    const commit = vi.fn()
    let completeTransition: (() => void) | undefined
    const transition = vi.fn((callback: () => void) => {
      completeTransition = callback
    })

    transitionToSection('#settings', commit, transition)

    expect(transition).toHaveBeenCalledOnce()
    expect(commit).not.toHaveBeenCalled()

    completeTransition?.()
    expect(commit).toHaveBeenCalledWith('settings')
  })
})
