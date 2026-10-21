import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'

export const themes = [
  { id: 'aurora', name: '静谧灰蓝', description: '低饱和灰蓝界面，柔和节点与品牌青紫，安静耐看。' },
  { id: 'academic', name: '学术暖金', description: '深炭灰与暖赭金，理性、克制，适合日常对话。' },
  { id: 'cool', name: '冷调青金', description: '海军炭黑与哑光灰绿，沉稳的智能体空间。' },
  { id: 'minimal', name: '极简单色', description: '中性灰与暗哑土金，专注代码和长文阅读。' },
] as const
export type Theme = typeof themes[number]['id']
const storageKey = 'jetaime.theme'
function validTheme(value: string | null): Theme {
  return themes.find(theme => theme.id === value)?.id ?? 'aurora'
}
const ThemeContext = createContext<{ theme: Theme; setTheme: (theme: Theme) => void } | null>(null)

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setTheme] = useState<Theme>(() => validTheme(document.documentElement.getAttribute('data-theme')))
  useEffect(() => {
    document.documentElement.dataset.theme = theme
    const background = getComputedStyle(document.documentElement).getPropertyValue('--background').trim()
    document.querySelector('meta[name="theme-color"]')?.setAttribute('content', background)
  }, [theme])
  useEffect(() => {
    function onStorage(event: StorageEvent) {
      if (event.key === storageKey || event.key === null) setTheme(validTheme(event.newValue))
    }
    window.addEventListener('storage', onStorage)
    return () => window.removeEventListener('storage', onStorage)
  }, [])
  function chooseTheme(value: Theme) {
    setTheme(value)
    try { localStorage.setItem(storageKey, value) } catch { /* Keep the in-memory preference when storage is unavailable. */ }
  }
  return <ThemeContext.Provider value={{ theme, setTheme: chooseTheme }}>{children}</ThemeContext.Provider>
}

export function useTheme() {
  const context = useContext(ThemeContext)
  if (!context) throw new Error('useTheme requires ThemeProvider')
  return context
}
