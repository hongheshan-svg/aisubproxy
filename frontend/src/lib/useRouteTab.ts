// 页内标签与 URL 的 ?tab= 同步：刷新不丢标签，面包屑第三级也读同一份定义。
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { PAGE_TABS, pickTab } from './adminui'

export function useRouteTab(path: string) {
  const route = useRoute()
  const router = useRouter()
  const defs = PAGE_TABS[path] || []
  const names = defs.map((t) => t.name)
  return computed<string>({
    get: () => pickTab(route.query.tab, names, names[0] || ''),
    set: (v) => {
      if (v === pickTab(route.query.tab, names, names[0] || '') && route.query.tab === v) return
      router.replace({ query: { ...route.query, tab: v } })
    },
  })
}
