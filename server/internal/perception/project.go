package perception

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ProjectState mirrors sensor/project.State for the perception consumer side.
// (Defined separately so perception doesn't import sensor/project — sensor
// packages must remain leaf nodes in the dependency graph.)
type ProjectState struct {
	Project       string    `json:"project"`
	Root          string    `json:"root,omitempty"`
	IndexedAt     time.Time `json:"indexed_at"`
	Language      string    `json:"language,omitempty"`
	Frameworks    []string  `json:"frameworks,omitempty"`
	BuildSystem   string    `json:"build_system,omitempty"`
	TestFramework string    `json:"test_framework,omitempty"`
	KeyFiles      []string  `json:"key_files,omitempty"`
	TopLevelDirs  []string  `json:"top_level_dirs,omitempty"`
}

// GetProjectState returns the most recently indexed snapshot for the given
// project. Returns nil if the project has never been indexed (sensor will
// fill it on the next hook event).
func (b *Bundler) GetProjectState(ctx context.Context, project string) (*ProjectState, error) {
	if project == "" {
		return nil, nil
	}
	// `root`, `language` are reserved — must be quoted.
	sql := fmt.Sprintf(
		`SELECT "root", "language", build_system, test_framework,
		        frameworks, key_files, module_summary,
		        CAST(ts AS BIGINT) AS ts_ms
		 FROM tma1_project_state
		 WHERE project = '%s'
		 ORDER BY ts DESC LIMIT 1`,
		escapeSQL(project),
	)
	_, rows, err := b.client.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	row := rows[0]

	st := &ProjectState{
		Project:       project,
		Root:          stringAt(row, 0),
		Language:      stringAt(row, 1),
		BuildSystem:   stringAt(row, 2),
		TestFramework: stringAt(row, 3),
		IndexedAt:     time.UnixMilli(int64At(row, 7)),
	}
	_ = json.Unmarshal([]byte(stringAt(row, 4)), &st.Frameworks)
	_ = json.Unmarshal([]byte(stringAt(row, 5)), &st.KeyFiles)

	// module_summary is a JSON blob: { "top_level_dirs": [...] }
	var mod struct {
		TopLevelDirs []string `json:"top_level_dirs"`
	}
	if err := json.Unmarshal([]byte(stringAt(row, 6)), &mod); err == nil {
		st.TopLevelDirs = mod.TopLevelDirs
	}
	return st, nil
}
