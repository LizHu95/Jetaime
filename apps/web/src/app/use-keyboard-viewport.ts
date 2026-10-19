import { useLayoutEffect, useRef } from 'react'

export function useKeyboardViewport() {
  const root = useRef<HTMLDivElement>(null)

  useLayoutEffect(() => {
    const element = root.current
    const viewport = window.visualViewport
    if (!element || !viewport) return
    let frame = 0
    let keyboardOpen = false
    let width = window.innerWidth
    let fullHeight = window.innerHeight

    const update = () => {
      // Zoom also shrinks the visual viewport; leave pinch zoom to the browser.
      if (Math.abs(viewport.scale - 1) > 0.05) return
      if (width !== window.innerWidth) {
        width = window.innerWidth
        fullHeight = window.innerHeight
      }
      fullHeight = Math.max(fullHeight, window.innerHeight, document.documentElement.clientHeight)
      const active = document.activeElement
      const editing = active instanceof HTMLElement && active.matches('textarea, input:not([type=checkbox]):not([type=radio]):not([type=button]):not([type=submit]), [contenteditable=true]')
      // Browser toolbars can change viewport height without opening a keyboard.
      keyboardOpen = (editing || keyboardOpen) && fullHeight - viewport.height > 150
      element.classList.toggle('keyboard-open', keyboardOpen)
      element.style.setProperty('--keyboard-viewport-height', `${viewport.height}px`)
      element.style.setProperty('--keyboard-viewport-top', `${viewport.offsetTop}px`)
      if (!keyboardOpen) fullHeight = window.innerHeight
    }
    const schedule = () => {
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(update)
    }
    update()
    viewport.addEventListener('resize', schedule)
    viewport.addEventListener('scroll', schedule)
    window.addEventListener('resize', schedule)
    document.addEventListener('focusin', schedule)
    document.addEventListener('focusout', schedule)
    return () => {
      cancelAnimationFrame(frame)
      viewport.removeEventListener('resize', schedule)
      viewport.removeEventListener('scroll', schedule)
      window.removeEventListener('resize', schedule)
      document.removeEventListener('focusin', schedule)
      document.removeEventListener('focusout', schedule)
      element.classList.remove('keyboard-open')
      element.style.removeProperty('--keyboard-viewport-height')
      element.style.removeProperty('--keyboard-viewport-top')
    }
  }, [])

  return root
}
