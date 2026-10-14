import type { Session } from '@/features/auth/schema'
export type Group = { id: string; peer: Session['user']; assistantName: string; unreadCount: number; lastReadSeq: number; lastMessageSeq: number; lastMessage: string }
export type AgentResult = { kind: 'qa' | 'extract'; promptVersion: string; answer?: { status: 'answered' | 'insufficient_evidence' | 'conflicting_evidence'; answer: string; sourceMessageIds: string[] }; suggestions?: { id: string; title: string; assigneeId: string | null; timeExpression: string; sourceMessageIds: string[] }[] }
export type Message = { id: string; groupId: string; seq: number; senderType: 'user' | 'assistant'; senderId: string | null; senderName: string; content: string; mentionsAI: boolean; clientMessageId: string | null; replyToId: string | null; createdAt: string; agentKind?: 'qa' | 'extract'; agentResult?: AgentResult }
export type ChatState = { id: string; members: Session['user'][]; lastSeq: number; readSeq: number; unreadCount: number; assistantName: string; aiEnabled: boolean }
export type MessagePage = { messages: Message[]; hasMore: boolean }
export type Run = { id: string; triggerMessageId: string; requestedBy: string; status: 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'; errorCode: string | null; responseMessageId: string | null }
export function mergeMessages(...lists: Message[][]) {
  const byId = new Map(lists.flat().map(message => [message.id, message]))
  return [...byId.values()].sort((a, b) => a.seq - b.seq)
}
