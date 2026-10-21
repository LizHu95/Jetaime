import type { Session } from '@/features/auth/schema'
export type Group = { id: string; peer: Session['user']; assistantName: string; unreadCount: number; lastReadSeq: number; lastMessageSeq: number; lastMessage: string }
type CardBase = { id: string; title: string; source: 'amap'; reason?: string }
export type PlaceCard = CardBase & { type: 'place'; place: { name: string; address: string; city?: string; costPerPerson: number | null; rating: number | null; openingHours?: string; mapUrl: string } }
export type RouteCard = CardBase & { type: 'route'; route: { mode: 'walking' | 'driving' | 'bicycling' | 'transit'; distanceMeters: number | null; durationSeconds: number | null; walkingDistanceMeters: number | null; instructions: string[]; mapUrl: string } }
export type AgentCard = PlaceCard | RouteCard
export type AgentResult = { kind: 'main' | 'help' | 'qa' | 'extract'; promptVersion: string; basis?: 'general' | 'history' | 'mixed'; route?: { kind: 'main' | 'help' | 'qa' | 'extract'; version: string }; answer?: { status: 'answered' | 'insufficient_evidence' | 'conflicting_evidence'; answer: string; sourceMessageIds: string[] }; suggestions?: { id: string; title: string; assigneeId: string | null; timeExpression: string; sourceMessageIds: string[] }[]; cards?: AgentCard[] }
export type Message = { id: string; groupId: string; seq: number; senderType: 'user' | 'assistant'; senderId: string | null; senderName: string; content: string; mentionsAI: boolean; clientMessageId: string | null; replyToId: string | null; createdAt: string; agentKind?: 'auto' | 'main' | 'help' | 'qa' | 'extract'; agentResult?: AgentResult }
export type ChatState = { id: string; members: Session['user'][]; lastSeq: number; readSeq: number; unreadCount: number; assistantName: string; aiEnabled: boolean; proactiveEnabled: boolean }
export type MessagePage = { messages: Message[]; hasMore: boolean }
export type Run = { id: string; triggerMessageId: string; requestedBy: string; status: 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'; errorCode: string | null; traceId?: string | null; attempt?: number; responseMessageId: string | null }
export function mergeMessages(...lists: Message[][]) {
  const byId = new Map(lists.flat().map(message => [message.id, message]))
  return [...byId.values()].sort((a, b) => a.seq - b.seq)
}
