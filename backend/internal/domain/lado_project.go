package domain

import "time"

// LadoProjectSettings are LADO's own settings for one project
// (docs/LJY-PRD/TDD.md §4.2). They live in lado_project_settings, not in
// projects.config, because AO replaces that JSON as a whole. A newly
// registered project gets stored settings at registration: LADO mode on
// unless tracker intake is on, and LadoNewProjectMaxConcurrent (#3). A
// project without stored settings, registered before that, has LADO mode off.
type LadoProjectSettings struct {
	ProjectID ProjectID
	// LadoMode puts the project's workers under the worker gate (TDD §6).
	LadoMode bool
	// MaxConcurrent caps the executions that run at once in the project:
	// LadoNewProjectMaxConcurrent for a newly registered project, and
	// LadoDefaultProjectMaxConcurrent for one without stored settings.
	MaxConcurrent int
	// CheckCommands are the commands LADO runs on each PR head in the
	// execution's worktree (docs/LJY-PRD/TDD.md §8.2), at most
	// LadoCheckCommandsMax, and CheckTimeoutMinutes the time limit of each,
	// LadoCheckTimeoutMin to LadoCheckTimeoutMax (LadoCheckTimeoutDefault
	// until it sets one).
	CheckCommands       []LadoCheckCommand
	CheckTimeoutMinutes int
	// OverlapWait holds a task back while a live run of another task of the
	// project may touch the same files (docs/LJY-PRD/TDD.md §5.4), and
	// OverlapIgnore lists the globs whose overlap does not count; nil means
	// LadoDefaultOverlapIgnore. New projects have OverlapWait off (#101);
	// a project that turned it on keeps it.
	OverlapWait   bool
	OverlapIgnore []string
	// RunDirectly starts New Task's "Run without plan review" switch on in
	// the project: on until the project turns it off. Each run is still
	// approved in the desktop app.
	RunDirectly bool
	UpdatedAt   time.Time
}

// OverlapIgnoreOrDefault returns the project's ignore globs, or the defaults
// while it set none.
func (s LadoProjectSettings) OverlapIgnoreOrDefault() []string {
	if s.OverlapIgnore == nil {
		return LadoDefaultOverlapIgnore
	}
	return s.OverlapIgnore
}
