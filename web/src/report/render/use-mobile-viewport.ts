import { useEffect, useState } from 'react'

/** 视口是否进入移动端投影区间。 */

const mobileBreakpoint = 768

export function useMobileViewport(breakpoint = mobileBreakpoint) {
  const [mobile, setMobile] = useState(() =>
    typeof window !== 'undefined' && window.innerWidth < breakpoint)
  useEffect(() => {
    const query = window.matchMedia(`(max-width: ${breakpoint - 1}px)`)
    const update = () => setMobile(query.matches)
    update()
    query.addEventListener('change', update)
    return () => query.removeEventListener('change', update)
  }, [breakpoint])
  return mobile
}

