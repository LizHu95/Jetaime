import { useSyncExternalStore } from 'react'
import { locationPreference } from './location'

function subscribe(onChange: () => void) {
  window.addEventListener('storage', onChange)
  window.addEventListener('jetaime:location-preference', onChange)
  return () => {
    window.removeEventListener('storage', onChange)
    window.removeEventListener('jetaime:location-preference', onChange)
  }
}

export function useLocationPreference(actor: string) {
  return useSyncExternalStore(subscribe, () => locationPreference(actor))
}
