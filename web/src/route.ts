import { useEffect, useState } from 'react'

// Hash routes: #/, #/dependencies, #/dependencies/<name>, #/services
function current(): string[] {
  return window.location.hash.replace(/^#\/?/, '').split('/').filter(Boolean)
}

export function navigate(path: string) {
  window.location.hash = path
}

export function useRoute(): string[] {
  const [route, setRoute] = useState(current)
  useEffect(() => {
    const onChange = () => setRoute(current())
    window.addEventListener('hashchange', onChange)
    return () => window.removeEventListener('hashchange', onChange)
  }, [])
  return route
}
