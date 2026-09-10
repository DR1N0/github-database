package base

// StartupProgressKind identifies an online startup stage.
type StartupProgressKind string

const (
	StartupBranchLookup  StartupProgressKind = "branch_lookup"
	StartupReplayListing StartupProgressKind = "replay_listing"
	StartupReplaySegment StartupProgressKind = "replay_segment"
)

// StartupProgress describes completed startup work without including data payloads.
type StartupProgress struct {
	Kind          StartupProgressKind
	Version       int
	Segment       int
	TotalSegments int
}

// StartupProgressFunc receives synchronous startup progress notifications.
type StartupProgressFunc func(StartupProgress)

func shouldReportStartupSegment(segment, total int) bool {
	return total > 0 && (segment == 1 || segment%10 == 0 || segment == total)
}
