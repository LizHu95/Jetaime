package ai

// SkillRef identifies an immutable server-selected strategy. It carries no
// model-authored instructions, tool permissions, or filesystem paths.
type SkillRef struct {
	ID        string `json:"id"`
	Version   string `json:"version"`
	Breakdown string `json:"breakdown"`
}
