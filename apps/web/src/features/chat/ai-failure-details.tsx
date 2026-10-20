import { Dialog } from 'radix-ui'
import { Button } from '@/components/ui/button'

const reasons: Record<string, string> = {
  ai_unavailable: 'AI 服务暂未启用，请稍后重试。',
  context_too_large: '聊天上下文超过了模型容量，请缩小提问范围后重新提问。',
  context_unavailable: '暂时无法读取这次提问所需的聊天记录，请稍后重试。',
  model_failed: '模型调用失败，暂时无法生成回复，请稍后重试。',
  invalid_model_response: 'AI 返回的内容未通过校验，请重试。',
  agent_failed: 'AI 处理这次提问时出现异常，请重试。',
}

export function AIFailureDetails({ errorCode }: { errorCode: string | null }) {
  return <Dialog.Root>
    <Dialog.Trigger asChild><button type="button" className="message-ai-failed" aria-label="查看失败原因">回复失败</button></Dialog.Trigger>
    <Dialog.Portal>
      <Dialog.Overlay className="ai-failure-overlay" />
      <Dialog.Content className="ai-failure-details">
        <Dialog.Title>AI 回复失败</Dialog.Title>
        <Dialog.Description>{(errorCode && reasons[errorCode]) || '暂时无法获取具体失败原因，请稍后重试。'}</Dialog.Description>
        <Dialog.Close asChild><Button variant="outline" size="sm">关闭</Button></Dialog.Close>
      </Dialog.Content>
    </Dialog.Portal>
  </Dialog.Root>
}
