// Package skills owns versioned, embedded task instructions selected by the server.
package skills

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"gopkg.in/yaml.v3"
)

//go:embed communication/*/SKILL.md
var files embed.FS

type definition struct {
	Name, Description, Version, Breakdown, Path string
}

// Discovery retains metadata only; instruction bodies are loaded upon activation.
var communication, registryErr = discover(files)

var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Validate fails startup if a bundled skill cannot be discovered safely.
func Validate() error { return registryErr }

// Route selects from SKILL.md metadata, without loading instruction bodies.
func Route(breakdown string) (ai.SkillRef, bool) {
	def, ok := communication[breakdown]
	return ai.SkillRef{ID: def.Name, Version: def.Version, Breakdown: def.Breakdown}, ok && registryErr == nil
}

// Load accepts an exact registered identity and version, not a file path or
// arbitrary instructions. Bodies are embedded and require a release to change.
func Load(ref ai.SkillRef) (string, error) {
	if registryErr != nil {
		return "", registryErr
	}
	registered, ok := communication[ref.Breakdown]
	if !ok || ref.ID != registered.Name || ref.Version != registered.Version {
		return "", fmt.Errorf("%w: unknown communication skill or version", ai.ErrInvalidDecision)
	}
	data, err := files.ReadFile(registered.Path)
	if err != nil {
		return "", fmt.Errorf("load communication skill: %w", err)
	}
	_, body, err := parseDocument(registered.Path, data)
	return body, err
}

func discover(source fs.FS) (map[string]definition, error) {
	paths, err := fs.Glob(source, "communication/*/SKILL.md")
	if err != nil || len(paths) == 0 {
		return nil, fmt.Errorf("no bundled communication SKILL.md packages found")
	}
	definitions := make(map[string]definition, len(paths))
	for _, name := range paths {
		data, err := fs.ReadFile(source, name)
		if err != nil {
			return nil, err
		}
		def, _, err := parseDocument(name, data)
		if err != nil {
			return nil, fmt.Errorf("invalid skill %s: %w", name, err)
		}
		if _, duplicate := definitions[def.Breakdown]; duplicate {
			return nil, fmt.Errorf("duplicate skill route for %s", def.Breakdown)
		}
		definitions[def.Breakdown] = def
	}
	return definitions, nil
}

// Agent Skills format: https://agentskills.io/specification. Version and
// breakdown are application requirements inside the standard metadata field.
func parseDocument(name string, data []byte) (definition, string, error) {
	invalid := func(reason string) (definition, string, error) {
		return definition{}, "", fmt.Errorf("%w: %s", ai.ErrInvalidDecision, reason)
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return invalid("SKILL.md requires YAML frontmatter")
	}
	header, body, ok := strings.Cut(rest, "\n---\n")
	if !ok || strings.TrimSpace(body) == "" {
		return invalid("SKILL.md requires closed frontmatter and instructions")
	}
	var document, extra yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(header))
	if err := decoder.Decode(&document); err != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return invalid("frontmatter must be a YAML mapping")
	}
	if err := decoder.Decode(&extra); err != io.EOF {
		return invalid("frontmatter must contain one YAML document")
	}
	fields, metadata := map[string]string{}, map[string]string{}
	root := document.Content[0]
	seen := map[string]bool{}
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if key.Tag != "!!str" || seen[key.Value] {
			return invalid("frontmatter has a non-string or duplicate key")
		}
		seen[key.Value] = true
		switch key.Value {
		case "metadata":
			if value.Kind != yaml.MappingNode {
				return invalid("metadata must be a string-to-string mapping")
			}
			for j := 0; j < len(value.Content); j += 2 {
				mk, mv := value.Content[j], value.Content[j+1]
				if mk.Tag != "!!str" || mv.Kind != yaml.ScalarNode || mv.Tag != "!!str" {
					return invalid("metadata keys and values must be strings")
				}
				if _, duplicate := metadata[mk.Value]; duplicate {
					return invalid("metadata contains duplicate keys")
				}
				metadata[mk.Value] = mv.Value
			}
		case "name", "description", "license", "compatibility", "allowed-tools":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
				return invalid("frontmatter field must be a string: " + key.Value)
			}
			fields[key.Value] = value.Value
		default:
			return invalid("unsupported frontmatter field: " + key.Value)
		}
	}
	def := definition{Name: fields["name"], Description: fields["description"], Version: metadata["version"], Breakdown: metadata["jetaime-breakdown"], Path: name}
	if len(def.Name) > 64 || !skillName.MatchString(def.Name) || def.Name != path.Base(path.Dir(name)) {
		return invalid("name must use kebab-case, fit 64 characters, and match its directory")
	}
	if strings.TrimSpace(def.Description) == "" || utf8.RuneCountInString(def.Description) > 1024 {
		return invalid("description must contain 1-1024 characters")
	}
	if compatibility, ok := fields["compatibility"]; ok && (strings.TrimSpace(compatibility) == "" || utf8.RuneCountInString(compatibility) > 500) {
		return invalid("compatibility must contain 1-500 characters")
	}
	if strings.TrimSpace(def.Version) == "" || strings.TrimSpace(def.Breakdown) == "" {
		return invalid("Jetaime requires metadata.version and metadata.jetaime-breakdown")
	}
	return def, strings.TrimSpace(body), nil
}
