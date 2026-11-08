package remindertool

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
)

// Check the current original, never the model's title or a peer's old request.
// A request to remind oneself to contact somebody else remains a self reminder.
func targetsAnotherRecipient(input ai.Context) bool {
	if input.Request == nil {
		return false
	}
	for _, m := range input.Messages {
		if m.ID != input.Request.MessageID || m.SenderID != input.ActorID {
			continue
		}
		before, after, found := strings.Cut(m.Text, "提醒")
		if !found {
			return false
		}
		after = strings.TrimSpace(after)
		for _, prefix := range []string{"一下", "下"} {
			after = strings.TrimSpace(strings.TrimPrefix(after, prefix))
		}
		for _, user := range input.Users {
			if user.ID == input.ActorID || user.Nickname == "" {
				continue
			}
			for _, prefix := range []string{"给", "替", "帮"} {
				if strings.Contains(before, prefix+user.Nickname) || strings.Contains(before, prefix+"@"+user.Nickname) {
					return true
				}
			}
		}
		for _, self := range []string{"我", "自己", "本人"} {
			if strings.HasPrefix(after, self) {
				return false
			}
		}
		for _, peer := range []string{"他", "她", "对方", "大家", "我们", "TA", "ta"} {
			if strings.HasPrefix(after, peer) {
				return true
			}
		}
		after = strings.TrimSpace(strings.TrimLeft(after, "@＠"))
		for _, user := range input.Users {
			if user.ID == input.ActorID || user.Nickname == "" {
				continue
			}
			if strings.HasPrefix(after, user.Nickname) {
				return true
			}
		}
	}
	return false
}

var (
	colonClock    = regexp.MustCompile(`([0-9]{1,2})\s*[:：]\s*([0-9]{2})`)
	wordClock     = regexp.MustCompile(`([0-9零〇一二两三四五六七八九十]{1,3})\s*(点|时)(半|一刻|三刻|([0-9零〇一二两三四五六七八九十]{1,3})\s*分?)?`)
	relativeClock = regexp.MustCompile(`([0-9零〇一二两三四五六七八九十百]+|半)\s*个?\s*(分钟|小时|秒钟|秒)\s*(以后|后)`)
	advanceClock  = regexp.MustCompile(`(提前|推迟)\s*([0-9零〇一二两三四五六七八九十百]+|半)\s*个?\s*(分钟|小时)`)
)

func chineseNumber(text string) (int, bool) {
	if n, err := strconv.Atoi(text); err == nil {
		return n, n >= 0 && n <= 999
	}
	digits := map[rune]int{'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	total, current := 0, 0
	for _, r := range text {
		if n, ok := digits[r]; ok {
			current = n
		} else if r == '十' || r == '百' {
			unit := 10
			if r == '百' {
				unit = 100
			}
			if current == 0 {
				current = 1
			}
			total += current * unit
			current = 0
		} else {
			return 0, false
		}
	}
	return total + current, text != ""
}

func durationExpression(number, unit string) (time.Duration, bool) {
	value := 0.5
	if number != "半" {
		n, ok := chineseNumber(number)
		if !ok {
			return 0, false
		}
		value = float64(n)
	}
	multiplier := time.Minute
	if unit == "小时" {
		multiplier = time.Hour
	}
	if strings.HasPrefix(unit, "秒") {
		multiplier = time.Second
	}
	return time.Duration(value * float64(multiplier)), true
}

// Parsed clocks must match the proposed time, not merely contain any time word.
// Bare 8点 is ambiguous; 08:00 is an explicit 24-hour clock. Unsupported or
// multiple distinct clocks fail closed instead of authorizing an arbitrary time.
func messageClocks(m ai.Message) (map[string]bool, bool) {
	clocks := map[string]bool{}
	hasExpression := false
	add := func(hour, minute, offset int, word bool) {
		hasExpression = true
		if hour > 23 || minute > 59 {
			return
		}
		prefix := m.Text[:offset]
		if i := strings.LastIndexAny(prefix, "，。；\n"); i >= 0 {
			prefix = prefix[i+len(string([]rune(prefix[i:])[0])):]
		}
		pm := strings.Contains(prefix, "下午") || strings.Contains(prefix, "晚上") || strings.Contains(prefix, "傍晚") || strings.Contains(prefix, "中午")
		am := strings.Contains(prefix, "上午") || strings.Contains(prefix, "早上") || strings.Contains(prefix, "凌晨")
		if hour < 12 && pm {
			hour += 12
		}
		if hour == 12 && (strings.Contains(prefix, "凌晨") || strings.Contains(prefix, "晚上")) {
			hour = 0
		}
		if word && hour > 0 && hour < 12 && !pm && !am {
			return
		}
		clocks[fmt.Sprintf("%02d:%02d", hour, minute)] = true
	}
	for _, match := range colonClock.FindAllStringSubmatchIndex(m.Text, -1) {
		h, _ := strconv.Atoi(m.Text[match[2]:match[3]])
		min, _ := strconv.Atoi(m.Text[match[4]:match[5]])
		add(h, min, match[0], false)
	}
	for _, match := range wordClock.FindAllStringSubmatchIndex(m.Text, -1) {
		h, ok := chineseNumber(m.Text[match[2]:match[3]])
		if !ok {
			continue
		}
		minute := 0
		if match[6] >= 0 {
			switch value := m.Text[match[6]:match[7]]; value {
			case "半":
				minute = 30
			case "一刻":
				minute = 15
			case "三刻":
				minute = 45
			default:
				minute, ok = chineseNumber(strings.TrimSpace(strings.TrimSuffix(value, "分")))
			}
		}
		if ok {
			add(h, minute, match[0], true)
		}
	}
	for _, match := range relativeClock.FindAllStringSubmatch(m.Text, -1) {
		hasExpression = true
		if duration, ok := durationExpression(match[1], match[2]); ok {
			clocks[m.SentAt.In(time.FixedZone("Asia/Shanghai", 8*3600)).Add(duration).Format("15:04")] = true
		}
	}
	for _, word := range []struct{ text, clock string }{{"正午", "12:00"}, {"午夜", "00:00"}, {"零点", "00:00"}} {
		if strings.Contains(m.Text, word.text) {
			hasExpression = true
			clocks[word.clock] = true
		}
	}
	return clocks, hasExpression
}

func hasExplicitTime(input ai.Context, clock string, sources ...string) bool {
	if input.Request == nil {
		return false
	}
	selected := map[string]bool{}
	for _, id := range sources {
		selected[id] = true
	}
	var current ai.Message
	var historical []ai.Message
	for _, m := range input.Messages {
		if m.SenderID != input.ActorID || m.GroupID != input.GroupID || m.SenderType == "assistant" || m.SentAt.After(input.Request.SentAt) {
			continue
		}
		if m.ID == input.Request.MessageID {
			current = m
		} else if selected[m.ID] {
			historical = append(historical, m)
		}
	}
	if clocks, expressed := messageClocks(current); expressed {
		return len(clocks) == 1 && clocks[clock]
	}
	adjustment := time.Duration(0)
	if match := advanceClock.FindStringSubmatch(current.Text); match != nil {
		var ok bool
		adjustment, ok = durationExpression(match[2], match[3])
		if !ok {
			return false
		}
		if match[1] == "提前" {
			adjustment = -adjustment
		}
	}
	// The latest cited arrangement overrides an earlier clock or existing draft.
	var latest *ai.Message
	for i := range historical {
		if _, expressed := messageClocks(historical[i]); expressed && (latest == nil || historical[i].SentAt.After(latest.SentAt)) {
			latest = &historical[i]
		}
	}
	if latest != nil {
		clocks, _ := messageClocks(*latest)
		for value := range clocks {
			parsed, _ := time.Parse("15:04", value)
			return len(clocks) == 1 && parsed.Add(adjustment).Format("15:04") == clock
		}
		return false
	}
	if input.ReminderDraft != nil && input.ReminderDraft.Time != "" {
		parsed, err := time.Parse("15:04", input.ReminderDraft.Time)
		return err == nil && parsed.Add(adjustment).Format("15:04") == clock
	}
	return false
}

// Missing fields may create a draft, but a cited concrete clock must not be
// discarded to bypass the exact-time check. Date-only draft follow-ups still
// preserve their previously validated clock.
func checkMissingTime(input ai.Context, sources []string) error {
	selected := map[string]bool{input.Request.MessageID: true}
	for _, id := range sources {
		selected[id] = true
	}
	for _, m := range input.Messages {
		if !selected[m.ID] || m.SenderID != input.ActorID || m.GroupID != input.GroupID || m.SenderType == "assistant" || m.SentAt.After(input.Request.SentAt) {
			continue
		}
		clocks, expressed := messageClocks(m)
		for clock := range clocks {
			if hasExplicitTime(input, clock, sources...) {
				return tools.InvalidArguments("time_evidence_omitted", "所引用的安排已经给出具体时刻，请按原文填写time，不能把已有时刻丢弃后重复追问。")
			}
		}
		if m.ID == input.Request.MessageID && expressed && len(clocks) != 1 && input.ReminderDraft != nil && input.ReminderDraft.Time != "" {
			return tools.InvalidArguments("time_needs_clarification", "本次修改的时刻有歧义，请用respond先澄清；不能沿用旧草稿时刻保存成已确定的提醒。")
		}
	}
	return nil
}
