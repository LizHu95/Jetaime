import { ArrowUpRight, MapPin, Route } from 'lucide-react'
import type { AgentCard, PlaceCard } from './types'

// Only backend-generated Amap destinations are actionable, including on old messages.
export function safeMapUrl(value: string): string | undefined {
  try {
    const url = new URL(value)
    if (url.protocol === 'https:' && url.host === 'uri.amap.com' && !url.username && !url.password && ['/marker', '/navigation'].includes(url.pathname)) return url.href
  } catch { /* Missing/malformed links are hidden. */ }
}
export function formatDistance(value: number | null | undefined) {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return undefined
  return value < 1000 ? `${Math.round(value)} 米` : `${Number((value / 1000).toFixed(1))} 公里`
}
export function formatDuration(value: number | null | undefined) {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return undefined
  const minutes = Math.ceil(value / 60)
  return minutes < 60 ? `${minutes} 分钟` : `${Math.floor(minutes / 60)} 小时${minutes % 60 ? ` ${minutes % 60} 分钟` : ''}`
}
export function routeQuestion(card: PlaceCard) {
  const place = card.place
  return `怎么去「${place.name}」${[place.city, place.address].filter(Boolean).length ? `（${[place.city, place.address].filter(Boolean).join('，')}）` : ''}，远不远？如果还不知道我的出发地，请先问我。`
}
function validNumber(value: number | null | undefined): value is number { return typeof value === 'number' && Number.isFinite(value) && value >= 0 }

export function AgentCards({ cards, onAsk, disabled = false }: { cards?: AgentCard[]; onAsk: (question: string) => void; disabled?: boolean }) {
  if (!Array.isArray(cards) || cards.length === 0) return null
  return <div className="agent-cards" aria-label="AI 推荐与路线">
    {cards.slice(0, 5).map(card => {
      if (!card || card.source !== 'amap') return null
      if (card.type === 'place' && card.place?.name) {
        const p = card.place, url = safeMapUrl(p.mapUrl)
        return <article className="agent-card" key={card.id} aria-label={`${p.name}推荐卡片`}>
          <div className="agent-card-heading"><MapPin size={17} aria-hidden="true" /><h3>{p.name}</h3></div>
          {card.reason && <p className="agent-card-reason">{card.reason}</p>}
          {(p.city || p.address) && <p className="agent-card-address">{[p.city, p.address].filter(Boolean).join(' · ')}</p>}
          <div className="agent-card-facts">
            {validNumber(p.costPerPerson) && <span>人均 ¥{p.costPerPerson}</span>}
            {validNumber(p.rating) && p.rating <= 5 && <span>评分 {p.rating}</span>}
          </div>
          {p.openingHours && <p className="agent-card-hours">营业时间 {p.openingHours}</p>}
          <div className="agent-card-footer"><small>来源：高德地图</small><div>
            {url && <a href={url} target="_blank" rel="noopener noreferrer">查看地图 <ArrowUpRight size={13} aria-hidden="true" /></a>}
            <button type="button" disabled={disabled} aria-label={`怎么去${p.name}`} title="填入路线追问，确认后发送" onClick={() => onAsk(routeQuestion(card))}>怎么去</button>
          </div></div>
        </article>
      }
      if (card.type === 'route' && card.route) {
        const r = card.route, url = safeMapUrl(r.mapUrl), distance = formatDistance(r.distanceMeters), duration = formatDuration(r.durationSeconds), walking = formatDistance(r.walkingDistanceMeters)
        const mode = { walking: '步行', driving: '驾车', bicycling: '骑行', transit: '公交地铁' }[r.mode]
        if (!mode) return null
        return <article className="agent-card agent-route-card" key={card.id} aria-label={`${mode}路线卡片`}>
          <div className="agent-card-heading"><Route size={17} aria-hidden="true" /><h3>{card.title || `${mode}路线`}</h3></div>
          <div className="agent-route-metrics">{duration && <strong>预计 {duration}</strong>}{distance && <span>{distance}</span>}</div>
          {card.reason && <p className="agent-card-reason">{card.reason}</p>}
          {walking && <p className="agent-card-address">其中步行 {walking}</p>}
          {Array.isArray(r.instructions) && r.instructions.length > 0 && <details className="agent-route-details"><summary>路线说明</summary><ol>{r.instructions.slice(0, 8).map((instruction, index) => <li key={index}>{instruction}</li>)}</ol></details>}
          <div className="agent-card-footer"><small>来源：高德地图 · 耗时为估算</small>{url && <a href={url} target="_blank" rel="noopener noreferrer">{url && new URL(url).pathname === '/marker' ? '查看目的地' : '查看路线'} <ArrowUpRight size={13} aria-hidden="true" /></a>}</div>
        </article>
      }
      return null
    })}
  </div>
}
