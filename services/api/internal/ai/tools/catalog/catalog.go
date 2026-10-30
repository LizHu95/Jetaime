// Package catalog is the branch-specific main-tool registration policy shared
// by production and replay. Dependencies are supplied by their entry points.
package catalog

import (
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/search"
)

func New(entries ...tools.Tool) (*tools.Registry, error) {
	entries = append(entries, search.New(entries))
	return tools.New(entries...)
}
