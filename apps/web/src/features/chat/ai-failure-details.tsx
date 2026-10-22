import { useState } from 'react'
import type { Run } from './types'
import { Dialog } from 'radix-ui'
import { Button } from '@/components/ui/button'

const reasons: Record<string, string> = {
  ai_unavailable: 'AI 服务暂未启用，请稍后重试。',
  context_too_large: '聊天上下文超过了模型容量，请缩小提问范围后重新提问。',
  context_unavailable: '暂时无法读取这次提问所需的聊天记录，请稍后重试。',
  model_failed: '模型调用失败，暂时无法生成回复，请稍后重试。',
  invalid_sources: 'AI 引用了不符合要求的消息来源，未能完成提醒设置，请重试。',
  time_evidence_missing: 'AI 填写的提醒时间缺少有效的原始消息依据，请重试。',
  invalid_model_response: 'AI 返回的内容未通过校验，请重试。',
  agent_failed: 'AI 处理这次提问时出现异常，请重试。',
}

export function AIFailureDetails({ run, groupId }: { run: Run; groupId: string }) {
  const [copyStatus, setCopyStatus] = useState('')
  const reason = (run.errorCode && reasons[run.errorCode]) || '暂时无法获取具体失败原因，请稍后重试。'
  const diagnostics = [
    'AI 回复失败',
    `原因: ${reason}`,
    `错误码: ${run.errorCode || 'unknown'}`,
    `Phoenix Trace ID: ${run.traceId || '未记录'}`,
    `Run ID: ${run.id}`,
    `Group ID: ${groupId}`,
    `Message ID: ${run.triggerMessageId}`,
    ...(run.attempt != null ? [`执行次数: ${run.attempt}`] : []),
  ].join('\n')
  async function copyDiagnostics() {
    try {
      await navigator.clipboard.writeText(diagnostics)
      setCopyStatus('诊断信息已复制')
    } catch {
      setCopyStatus('复制失败，请长按或选中上方信息复制。')
    }
  }
  return <Dialog.Root onOpenChange={() => setCopyStatus('')}>
    <Dialog.Trigger asChild><button type="button" className="message-ai-failed" aria-label="查看失败原因">回复失败</button></Dialog.Trigger>
    <Dialog.Portal>
      <Dialog.Overlay className="ai-failure-overlay" />
      <Dialog.Content className="ai-failure-details">
        <Dialog.Title>AI 回复失败</Dialog.Title>
        <Dialog.Description>{reason}</Dialog.Description>
        <pre className="ai-failure-diagnostics" tabIndex={0} aria-label="诊断信息">{diagnostics}</pre>
        {!run.traceId && <p>此记录没有可用的 Trace ID，可用任务 ID 辅助排查。</p>}
        <p role="status" aria-live="polite">{copyStatus}</p>
        <div className="ai-failure-actions">
          <Button variant="outline" size="sm" onClick={() => { void copyDiagnostics() }}>复制诊断信息</Button>
          <Dialog.Close asChild><Button variant="outline" size="sm">关闭</Button></Dialog.Close>
        </div>
      </Dialog.Content>
    </Dialog.Portal>
  </Dialog.Root>
}
