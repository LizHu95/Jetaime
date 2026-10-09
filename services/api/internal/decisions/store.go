package decisions

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/LizHu95/Jetaime/services/api/internal/memory"
	"github.com/LizHu95/Jetaime/services/api/internal/notes"
	"github.com/LizHu95/Jetaime/services/api/internal/space"
)

var (
	ErrNotFound     = errors.New("not found")                                    // 指定的决策或会话不存在。
	ErrForbidden    = errors.New("not authorized in this space")                 // 操作人没有当前空间或决策的访问权限。
	ErrStateChanged = errors.New("state changed; reload or start a new session") // 读取后的状态已变化，或操作与当前状态不匹配。
	ErrBusy         = errors.New("generation already running")                   // 同一换批命令正在执行，应等待而非重复生成。
)

// Dataset 是当前内存实现的完整基础数据集，不是某次决策请求。
// 列表代表多条空间、成员、笔记等记录，buildContext 再按权限筛选。
// 初始化后保持不变；真实数据库的增删改查尚未实现。
// 它是多类基础记录的集合，各类记录通过 ID 关联，不是数据库表的定义。
type Dataset struct {
	Spaces      []space.Space     `json:"spaces"`      // 空间记录：确定本次决策属于哪个个人或情侣空间。
	Members     []space.Member    `json:"members"`     // 成员关系：通过 SpaceID、UserID 判断空间参与者及访问资格。
	Notes       []notes.Note      `json:"notes"`       // 笔记本体：保存作者、类型和内容，同一笔记可以被多个空间收藏。
	Collections []notes.SpaceNote `json:"collections"` // 收藏关系：通过 SpaceID、NoteID 关联空间与笔记，不复制笔记本体。
	Memories    []memory.Memory   `json:"memories"`    // 用户记忆：使用前还需检查归属、确认状态及当前空间的使用授权。
}

// MemoryStore 在当前进程内保存决策历史、会话和可重试的换批命令。
// 版本检查防止耗时生成覆盖期间发生的新反馈；未来数据库实现需保留原子提交语义。
// 当前版本号是全局的，不同会话的并发写入也可能导致版本冲突。
// data 提供基础资料，下面三个 map 保存流程运行后产生的状态；进程退出后均不保留。
type MemoryStore struct {
	mu        sync.Mutex              // 保护数据读取与提交；调用模型时不持有这把锁。
	data      Dataset                 // Store 自己持有的副本，避免外部修改传入数据。
	revision  uint64                  // 读取时记下版本，写入时要求版本仍一致。
	decisions map[string]Decision     // 键为 Decision.ID；保存每一批的需求、候选快照、结果和反馈历史。
	sessions  map[string]Session      // 键为 Session.ID；保存跨批次的已推荐、已排除、当前采纳等状态。
	commands  map[string]batchCommand // 键由原决策 ID 与反馈事件 ID 组合；用于换批去重与失败重试。
}

// batchCommand 记录换批进度：执行中、失败可重试、完成后复用 ResultID。
// 它是存储层的内部控制记录，不是推荐方案，也不是对外返回的业务实体。
// Running=true：正在生成；Running=false 且 ResultID 为空：失败后允许重试；
// Running=false 且 ResultID 非空：已完成，重试时读取已有决策。
type batchCommand struct {
	DecisionID string // 发起换批时所针对的原决策 ID，用于确认重试仍属于同一次操作。
	ActorID    string // 发起换批的操作人 ID，可能与会话最初的发起人不同。
	ResultID   string // 换批成功后生成的新 Decision.ID；空值表示尚无成功结果。
	Running    bool   // 是否正在执行，避免同一命令被同时执行多次。
}

// NewMemoryStore 先复制并校验基础数据，再初始化历史、会话和命令索引。
func NewMemoryStore(data Dataset) (*MemoryStore, error) {
	owned, err := copyValue(data)
	if err != nil {
		return nil, err
	}
	for _, s := range owned.Spaces {
		var members []space.Member
		for _, m := range owned.Members {
			if m.SpaceID == s.ID {
				members = append(members, m)
			}
		}
		if err := s.ValidateMembers(members); err != nil {
			return nil, err
		}
	}
	for _, n := range owned.Notes {
		if err := n.Validate(); err != nil {
			return nil, err
		}
	}
	for _, m := range owned.Memories {
		if err := m.Validate(); err != nil {
			return nil, err
		}
	}
	return &MemoryStore{data: owned, decisions: map[string]Decision{}, sessions: map[string]Session{}, commands: map[string]batchCommand{}}, nil
}

// copyValue 用 JSON 做深复制，使嵌套切片和 map 也不与 Store 共享引用。
// 这是当前数据类型下的简易实现，传入类型必须支持 JSON 序列化。
func copyValue[T any](value T) (T, error) {
	var result T
	encoded, err := json.Marshal(value)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(encoded, &result)
	return result, err
}

// snapshot 在同一把锁内读取数据、会话和版本，返回可独立修改的副本。
// 新会话的 sessionID 为空，返回 nil 会话；权限与过期检查由服务层负责。
func (s *MemoryStore) snapshot(sessionID string) (Dataset, *Session, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := copyValue(s.data)
	if err != nil {
		return Dataset{}, nil, 0, err
	}
	var session *Session
	if sessionID != "" {
		value, ok := s.sessions[sessionID]
		if !ok {
			return Dataset{}, nil, 0, ErrNotFound
		}
		cloned, err := copyValue(value)
		if err != nil {
			return Dataset{}, nil, 0, err
		}
		session = &cloned
	}
	return data, session, s.revision, nil
}

// commitGenerated 将新决策、更新后的会话和换批完成状态一起提交。
// 若读取之后发生过其他写入，则拒绝这次提交，避免覆盖较新的状态。
func (s *MemoryStore) commitGenerated(d Decision, session Session, revision uint64, commandID string) error {
	d, err := copyValue(d)
	if err != nil {
		return err
	}
	session, err = copyValue(session)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revision != revision {
		return ErrStateChanged
	}
	if _, exists := s.decisions[d.ID]; exists {
		return ErrStateChanged
	}
	s.decisions[d.ID], s.sessions[session.ID] = d, session
	if commandID != "" {
		command := s.commands[commandID]
		command.Running, command.ResultID = false, d.ID
		s.commands[commandID] = command
	}
	s.revision++
	return nil
}

// decisionSnapshot 为历史查询或反馈提供同一时刻的数据副本。
// Command 仅在指定换批命令已经登记时存在，用来识别重试。
// 它是一次读取的返回容器，不会作为新的业务记录存储；修改副本不等于提交。
// Decision 是某一批历史，Session 是该批所属会话的当前状态，两者时间含义不同。
type decisionSnapshot struct {
	Data     Dataset       // 基础资料副本，服务层用来校验当前成员身份、笔记权限和约束。
	Decision Decision      // loadDecision 返回指定批次；loadSession 返回最新批次，没有时为零值。
	Session  Session       // 会话当前状态，包含跨批次的排除与采纳信息。
	Revision uint64        // 读取时的 Store 版本，提交时比较它以检测并发修改。
	Command  *batchCommand // 可选命令副本；nil 表示此次读取没有找到对应的已登记命令。
}

// loadDecision 同时读取指定决策及其当前会话，避免分别读取时状态不一致。
func (s *MemoryStore) loadDecision(decisionID, commandID string) (decisionSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	decision, ok := s.decisions[decisionID]
	if !ok {
		return decisionSnapshot{}, ErrNotFound
	}
	session, ok := s.sessions[decision.SessionID]
	if !ok {
		return decisionSnapshot{}, ErrNotFound
	}
	snapshot := decisionSnapshot{Data: s.data, Decision: decision, Session: session, Revision: s.revision}
	if command, exists := s.commands[commandID]; exists {
		snapshot.Command = &command
	}
	return copyValue(snapshot)
}

// loadSession 读取会话与最新一轮决策，供服务层校验访问权限。
func (s *MemoryStore) loadSession(sessionID string) (decisionSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return decisionSnapshot{}, ErrNotFound
	}
	snapshot := decisionSnapshot{Data: s.data, Session: session, Revision: s.revision}
	if session.LatestDecisionID != "" {
		decision, ok := s.decisions[session.LatestDecisionID]
		if !ok {
			return decisionSnapshot{}, ErrNotFound
		}
		snapshot.Decision = decision
	}
	return copyValue(snapshot)
}

// commitFeedback 原子保存反馈历史和会话投影；相同事件重试不增加版本号。
func (s *MemoryStore) commitFeedback(decision Decision, session Session, revision uint64, retry bool) error {
	decision, err := copyValue(decision)
	if err != nil {
		return err
	}
	session, err = copyValue(session)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revision != revision {
		return ErrStateChanged
	}
	s.decisions[decision.ID], s.sessions[session.ID] = decision, session
	if !retry {
		s.revision++
	}
	return nil
}

// commitBatchRequest 在生成前登记换批事件和执行状态，防止同一命令重复生成。
func (s *MemoryStore) commitBatchRequest(decision Decision, session Session, revision uint64, commandID, actorID string) error {
	decision, err := copyValue(decision)
	if err != nil {
		return err
	}
	session, err = copyValue(session)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revision != revision {
		return ErrStateChanged
	}
	s.decisions[decision.ID], s.sessions[session.ID] = decision, session
	s.commands[commandID] = batchCommand{DecisionID: decision.ID, ActorID: actorID, Running: true}
	s.revision++
	return nil
}

// markBatchFailed 保留命令记录并解除执行中状态，下一次可用同一事件 ID 重试。
func (s *MemoryStore) markBatchFailed(commandID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	command := s.commands[commandID]
	command.Running = false
	s.commands[commandID] = command
	s.revision++
}
