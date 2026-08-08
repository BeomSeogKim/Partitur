// Package protectedpath defines the protected Partitur locations and their
// consumer-specific representations.
package protectedpath

type definition struct {
	worktreeName string
	capture      string
	glob         string
}

var (
	partiturDirectory = definition{
		worktreeName: ".partitur",
		capture:      ":(exclude).partitur/**",
		glob:         ".partitur/**",
	}
	rootScore = definition{
		worktreeName: "partitur.yaml",
		capture:      ":(exclude)partitur.yaml",
		glob:         "partitur.yaml",
	}
	gitDirectory = definition{worktreeName: ".git"}
	partiturRefs = definition{glob: "refs/partitur/**"}
)

// WorktreeNames returns the protected worktree names that must be observed
// with Lstat. Cast coverage is derived: .partitur/cast.yaml is contained by
// .partitur, while the user cast is outside a worktree.
func WorktreeNames() []string {
	return names(rootScore, partiturDirectory)
}

// SnapshotNames returns the protected worktree names preserved for attempt
// rollback, including the .git indirection.
func SnapshotNames() []string {
	return names(gitDirectory, rootScore, partiturDirectory)
}

// CaptureExclusions returns the git pathspecs excluded from change-set capture.
func CaptureExclusions() []string {
	return captures(partiturDirectory, rootScore)
}

// AdvertisedGlobs returns the protected_paths payload for briefs and A.5.
func AdvertisedGlobs() []string {
	return globs(partiturDirectory, rootScore, partiturRefs)
}

func names(items ...definition) []string {
	result := make([]string, len(items))
	for index, item := range items {
		result[index] = item.worktreeName
	}
	return result
}

func captures(items ...definition) []string {
	result := make([]string, len(items))
	for index, item := range items {
		result[index] = item.capture
	}
	return result
}

func globs(items ...definition) []string {
	result := make([]string, len(items))
	for index, item := range items {
		result[index] = item.glob
	}
	return result
}
