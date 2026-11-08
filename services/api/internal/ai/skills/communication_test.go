package skills

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
)

func TestCommunicationRegistryDiscoversStandardSkillPackages(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
	paths, err := fs.Glob(files, "communication/*/SKILL.md")
	if err != nil || len(paths) != 5 {
		t.Fatal("expected five embedded strategies", paths, err)
	}
	ids := map[string]bool{}
	for _, breakdown := range []string{"understanding_mismatch", "support_mismatch", "communication_escalation", "pacing_conflict", "repair_blocked"} {
		ref, ok := Route(breakdown)
		if !ok || ref.Breakdown != breakdown || ref.Version == "" || ids[ref.ID] {
			t.Fatalf("missing or ambiguous route: %+v", ref)
		}
		ids[ref.ID] = true
		body, err := Load(ref)
		if err != nil {
			t.Fatal(err)
		}
		def := communication[breakdown]
		if def.Name != ref.ID || def.Version != ref.Version || def.Description == "" || strings.HasPrefix(body, "---") || strings.TrimSpace(body) == "" {
			t.Fatalf("metadata discovery or progressive loading failed: %+v", def)
		}
	}
	if _, ok := Route("conflict"); ok {
		t.Fatal("general collaboration breakdown mapped to a communication strategy")
	}
}

func TestSkillLoadRejectsUnregisteredIdentityVersionAndPath(t *testing.T) {
	ref, _ := Route("pacing_conflict")
	for _, mutate := range []func(*ai.SkillRef){
		func(r *ai.SkillRef) { r.ID = "../../prompts" },
		func(r *ai.SkillRef) { r.ID = "check-understanding" },
		func(r *ai.SkillRef) { r.Version = "v999" },
		func(r *ai.SkillRef) { r.Breakdown = "support_mismatch" },
		func(r *ai.SkillRef) { *r = ai.SkillRef{} },
	} {
		invalid := ref
		mutate(&invalid)
		if _, err := Load(invalid); !errors.Is(err, ai.ErrInvalidDecision) {
			t.Fatalf("unregistered strategy accepted: %+v %v", invalid, err)
		}
	}
}

func TestSkillFrontmatterValidation(t *testing.T) {
	valid := "---\nname: test-skill\ndescription: 处理测试互动。用于验证元数据加载。\nmetadata:\n  version: \"v2\"\n  jetaime-breakdown: support_mismatch\n---\n\n# 自包含的任务说明\n按本轮证据处理。\n"
	path := "communication/test-skill/SKILL.md"
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"no frontmatter", "# Instructions"},
		{"unclosed frontmatter", "---\nname: test-skill"},
		{"missing description", strings.Replace(valid, "description: 处理测试互动。用于验证元数据加载。\n", "", 1)},
		{"empty description", strings.Replace(valid, "description: 处理测试互动。用于验证元数据加载。", "description: ''", 1)},
		{"directory mismatch", strings.Replace(valid, "name: test-skill", "name: other-skill", 1)},
		{"underscore name", strings.Replace(valid, "name: test-skill", "name: test_skill", 1)},
		{"consecutive hyphens", strings.Replace(valid, "name: test-skill", "name: test--skill", 1)},
		{"description type", strings.Replace(valid, "description: 处理测试互动。用于验证元数据加载。", "description: [one, two]", 1)},
		{"numeric metadata", strings.Replace(valid, "version: \"v2\"", "version: 2", 1)},
		{"missing application route", strings.Replace(valid, "  jetaime-breakdown: support_mismatch\n", "", 1)},
		{"missing version", strings.Replace(valid, "  version: \"v2\"\n", "", 1)},
		{"duplicate field", strings.Replace(valid, "name: test-skill", "name: test-skill\nname: test-skill", 1)},
		{"duplicate metadata", strings.Replace(valid, "version: \"v2\"", "version: \"v2\"\n  version: \"v3\"", 1)},
		{"unknown top-level field", strings.Replace(valid, "name: test-skill", "name: test-skill\nversion: v2", 1)},
		{"invalid yaml", strings.Replace(valid, "name: test-skill", "name: [", 1)},
		{"empty instructions", strings.Split(valid, "\n# ")[0]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := parseDocument(path, []byte(tc.content)); !errors.Is(err, ai.ErrInvalidDecision) {
				t.Fatal("invalid skill accepted", err)
			}
		})
	}
	// Version and route come from supported custom metadata, never a Go table.
	def, body, err := parseDocument(path, []byte(strings.ReplaceAll(valid, "v2", "release-3")))
	if err != nil || def.Name != "test-skill" || def.Version != "release-3" || def.Breakdown != "support_mismatch" || strings.Contains(body, "metadata:") {
		t.Fatal("frontmatter not separated from instructions", def, err)
	}
	for _, count := range []int{1024, 1025} {
		content := strings.Replace(valid, "处理测试互动。用于验证元数据加载。", strings.Repeat("中", count), 1)
		_, _, err := parseDocument(path, []byte(content))
		if (err != nil) != (count > 1024) {
			t.Fatal("description limit must count characters", count, err)
		}
	}
	_, err = discover(fstest.MapFS{
		path:                                 {Data: []byte(valid)},
		"communication/other-skill/SKILL.md": {Data: []byte(strings.Replace(valid, "name: test-skill", "name: other-skill", 1))},
	})
	if err == nil {
		t.Fatal("ambiguous breakdown route accepted")
	}
}
