// Package demo supplies entirely fictional fixtures and deterministic adapters.
package demo

import (
	"fmt"

	"github.com/LizHu95/Jetaime/services/api/internal/decisions"
	"github.com/LizHu95/Jetaime/services/api/internal/memory"
	"github.com/LizHu95/Jetaime/services/api/internal/notes"
	"github.com/LizHu95/Jetaime/services/api/internal/space"
)

const (
	UserA       = "user-a"
	UserB       = "user-b"
	CoupleSpace = "couple-a-b"
	PersonalA   = "personal-a"
	PersonalB   = "personal-b"
)

// Fact is a manually verified fictional fact, not a model inference. Pointer
// values distinguish unknown from zero; total quotes apply to People participants.
type Fact struct {
	Verified           bool     `json:"verified"`
	Source             string   `json:"source"`
	PerPersonCostCents *int64   `json:"perPersonCostCents"`
	TotalCostCents     *int64   `json:"totalCostCents"`
	People             int      `json:"people"`
	DurationMinutes    *int     `json:"durationMinutes"`
	IngredientsKnown   bool     `json:"ingredientsKnown"`
	Ingredients        []string `json:"ingredients"`
	Indoor             *bool    `json:"indoor"`
	Accessible         *bool    `json:"accessible"`
}

type Scenario struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Request     decisions.Request `json:"request"`
	Expected    decisions.Outcome `json:"expected"`
}

type Fixture struct {
	Dataset   decisions.Dataset `json:"dataset"`
	Facts     map[string]Fact   `json:"facts"`
	Scenarios []Scenario        `json:"scenarios"`
}

func pointer[T any](value T) *T { return &value }

func NewFixture() Fixture {
	f := Fixture{Facts: map[string]Fact{}}
	f.Dataset.Spaces = []space.Space{
		{ID: PersonalA, Type: space.TypePersonal, Name: "A 的个人空间", CreatorID: UserA},
		{ID: PersonalB, Type: space.TypePersonal, Name: "B 的个人空间", CreatorID: UserB},
		{ID: CoupleSpace, Type: space.TypeCouple, Name: "A 与 B 的共同空间", CreatorID: UserA},
	}
	f.Dataset.Members = []space.Member{
		{SpaceID: PersonalA, UserID: UserA, Role: space.RoleOwner},
		{SpaceID: PersonalB, UserID: UserB, Role: space.RoleOwner},
		{SpaceID: CoupleSpace, UserID: UserA, Role: space.RoleOwner},
		{SpaceID: CoupleSpace, UserID: UserB, Role: space.RoleMember},
	}
	groups := []struct {
		kind  notes.Type
		count int
		title string
	}{
		{notes.TypeRestaurant, 20, "虚构餐厅"}, {notes.TypeRecipe, 15, "虚构菜谱"},
		{notes.TypeActivity, 10, "虚构活动"}, {notes.TypeMovie, 5, "虚构电影"},
	}
	for _, group := range groups {
		for i := 1; i <= group.count; i++ {
			id := fmt.Sprintf("%s-%02d", group.kind, i)
			author, personal := UserA, PersonalA
			if i%2 == 0 {
				author, personal = UserB, PersonalB
			}
			tags := []string{"清淡", "安静", "室内"}
			if i%2 == 0 {
				tags = []string{"辣", "户外"}
			}
			if group.kind == notes.TypeMovie {
				tags = []string{"喜剧", "室内"}
			}
			n := notes.Note{ID: id, CreatorID: author, Title: fmt.Sprintf("%s %02d", group.title, i), Type: group.kind, Description: "仅用于本地验证的虚构内容，不对应真实商户、菜谱或上映信息。", Tags: tags}
			f.Dataset.Notes = append(f.Dataset.Notes, n)
			f.Dataset.Collections = append(f.Dataset.Collections, notes.SpaceNote{SpaceID: personal, NoteID: id, AddedBy: author})
			// One note stays private even when both people are members of the couple space.
			if id != "restaurant-20" {
				f.Dataset.Collections = append(f.Dataset.Collections, notes.SpaceNote{SpaceID: CoupleSpace, NoteID: id, AddedBy: author})
			}
			fact := Fact{Verified: true, Source: "人工定义的虚构资料 v1", PerPersonCostCents: pointer(int64(4000 + i%6*1000)), DurationMinutes: pointer(40 + i%3*10), Indoor: pointer(i%2 != 0), Ingredients: []string{}}
			if group.kind == notes.TypeRestaurant || group.kind == notes.TypeRecipe {
				fact.IngredientsKnown = i%4 != 0
				if i%5 == 0 {
					fact.Ingredients = []string{"花生"}
				}
				if i%7 == 0 {
					fact.Ingredients = append(fact.Ingredients, "海鲜")
				}
			}
			if group.kind == notes.TypeRecipe {
				fact.PerPersonCostCents = nil
				fact.TotalCostCents, fact.People = pointer(int64(2000+i*100)), 2
			}
			if group.kind == notes.TypeMovie {
				fact.DurationMinutes = pointer(90 + i*5)
			}
			if id == "restaurant-20" {
				fact.PerPersonCostCents = nil
			}
			f.Facts[id] = fact
		}
	}
	a := []string{"喜欢辣", "喜欢安静", "喜欢户外", "喜欢喜剧", "喜欢清淡", "喜欢室内", "偏好简单准备", "偏好少排队", "愿意尝试新内容", "私有：偏爱深夜出行", "推断：喜欢高价餐厅", "上次电影一般"}
	b := []string{"不吃花生", "喜欢清淡", "喜欢安静", "喜欢室内", "喜欢喜剧", "偏好短活动", "偏好简单准备", "愿意尝试新内容", "推断：喜欢辣", "私有：偏好独自出行", "推断：喜欢户外", "上次活动一般"}
	for _, user := range []struct {
		id       string
		contents []string
	}{{UserA, a}, {UserB, b}} {
		for i, content := range user.contents {
			m := memory.Memory{ID: fmt.Sprintf("%s-memory-%02d", user.id, i+1), UserID: user.id, Type: memory.TypePreference, Content: content, Strength: memory.StrengthMedium, Source: memory.SourceExplicit, AllowedSpaceIDs: []string{CoupleSpace}}
			if user.id == UserB && i == 0 {
				m.Type, m.Strength = memory.TypeConstraint, memory.StrengthHard
			}
			if i == 9 {
				m.AllowedSpaceIDs = []string{}
			}
			if i == 10 {
				m.Source = memory.SourceInferred
			}
			if user.id == UserB && i == 8 {
				m.Source, m.Confirmed = memory.SourceInferred, true
			}
			if i == 11 {
				m.Source, m.Type, m.Strength = memory.SourceFeedback, memory.TypeFeedback, memory.StrengthLow
			}
			f.Dataset.Memories = append(f.Dataset.Memories, m)
		}
	}
	request := func(kind notes.Type) decisions.Request {
		return decisions.Request{SpaceID: CoupleSpace, RequesterID: UserA, Task: decisions.TaskSelect, Query: "今晚两个人做什么？", Conditions: decisions.Conditions{NoteTypes: []notes.Type{kind}, HardConstraints: []string{}, SoftPreferences: []string{}}}
	}
	normal := request(notes.TypeRestaurant)
	normal.Query = "今晚两个人吃什么？"
	normal.Conditions.BudgetMaxCents, normal.Conditions.DurationMaxMinutes = pointer(int64(15000)), pointer(60)
	conflict := request(notes.TypeRestaurant)
	conflict.Conditions.BudgetMaxCents = pointer(int64(0))
	unknown := request(notes.TypeMovie)
	unknown.Conditions.HardConstraints = []string{"必须无障碍"}
	empty := request(notes.TypeOther)
	personal := request(notes.TypeRestaurant)
	personal.SpaceID, personal.Query = PersonalA, "我自己吃什么？"
	contradiction := request(notes.TypeActivity)
	contradiction.Conditions.HardConstraints = []string{"必须室内", "必须户外"}
	f.Scenarios = []Scenario{
		{"normal", "两人晚餐：总预算150元、60分钟，B不吃花生", normal, decisions.OutcomeRecommended},
		{"conflict", "显式零预算：候选均有确定费用", conflict, decisions.OutcomeConstraintConflict},
		{"unknown", "必须无障碍：候选缺少已确认设施信息", unknown, decisions.OutcomeInsufficientInfo},
		{"empty", "当前空间没有 other 类型收藏", empty, decisions.OutcomeNoCandidates},
		{"personal", "个人决策：只读取本人收藏与记忆", personal, decisions.OutcomeRecommended},
		{"contradiction", "同时要求室内和户外：已确认条件互相冲突", contradiction, decisions.OutcomeConstraintConflict},
		{"short", "电影候选5条：第二批只剩2条未看选项", request(notes.TypeMovie), decisions.OutcomeRecommended},
	}
	return f
}

func (f Fixture) Scenario(name string) (Scenario, error) {
	for _, scenario := range f.Scenarios {
		if scenario.Name == name {
			return scenario, nil
		}
	}
	return Scenario{}, fmt.Errorf("unknown scenario %q", name)
}
