package ai

// CommunicationHelp is selected by the server from a recognized breakdown,
// rather than accepting model-generated instructions as an execution policy.
type CommunicationHelp struct {
	Action      string
	Instruction string
}

func HelpForCommunication(breakdown string) (CommunicationHelp, bool) {
	switch breakdown {
	case "understanding_mismatch":
		return CommunicationHelp{"check_understanding", "根据本人明确表达复述分歧，请本人或对方确认理解；不猜测隐含动机。"}, true
	case "support_mismatch":
		return CommunicationHelp{"clarify_support", "依据已表达的支持需要，帮助确认此刻希望被倾听、安慰还是一起想办法；需要已经说清时帮助对方接住，不重复询问。"}, true
	case "communication_escalation":
		return CommunicationHelp{"focus_on_event", "把讨论带回具体事件、本人已表达的感受和可执行请求；不公开批评表达方式，不要求受伤害者承担同等责任。"}, true
	case "pacing_conflict":
		return CommunicationHelp{"negotiate_pause", "尊重明确的暂停请求，帮助协商何时恢复沟通；时间由双方确认，不强迫继续，也不擅自创建提醒。"}, true
	case "repair_blocked":
		return CommunicationHelp{"clarify_unresolved_need", "区分已经回应和仍未回应的具体诉求，帮助说明需要什么回应或补救；不催促接受道歉、原谅或和好。"}, true
	default:
		return CommunicationHelp{}, false
	}
}
