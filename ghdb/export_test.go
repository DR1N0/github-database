// export_test.go exposes internal symbols to the ghdb_test external test package.
package ghdb

import (
	"context"
	"io/fs"

	"github.com/DR1N0/github-database/ghdb/base"
	"github.com/DR1N0/github-database/ghdb/github"
	"github.com/DR1N0/github-database/ghdb/graph"
	"github.com/DR1N0/github-database/ghdb/table"
)

// engineOf extracts the base.Internal engine from a DB (table or graph).
func engineOf(db DB) base.Engine {
	if ts, ok := db.(table.TableDB); ok {
		return table.GetInternal(ts)
	}
	if gs, ok := db.(graph.GraphDB); ok {
		return graph.GetInternal(gs)
	}
	panic("ghdb: engineOf: unknown DB type")
}

var (
	// OpenWithClient injects a pre-built GitHub client, bypassing token auth. For tests only.
	OpenWithClient = func(baseline fs.FS, gh github.Interface) (DB, error) {
		return NewOption(baseline).withClient(gh).Open()
	}

	MarshalJSONL   = base.MarshalJSONL
	UnmarshalJSONL = base.UnmarshalJSONL

	marshalJSONL   = base.MarshalJSONL
	unmarshalJSONL = base.UnmarshalJSONL

	Flush = func(db DB, ctx context.Context) error {
		return base.FlushEngine(engineOf(db), ctx)
	}
	Poll = func(db DB, ctx context.Context) error {
		return base.PollEngine(engineOf(db), ctx)
	}
	SetNextVerExists = func(db DB, v bool) {
		base.SetEngineNextVerExists(engineOf(db), v)
	}
	NextVerExists = func(db DB) bool {
		return base.EngineNextVerExists(engineOf(db))
	}
	WriteVer = func(db DB) int {
		return base.EngineWriteVer(engineOf(db))
	}
	InstanceID = func(db DB) string {
		return base.EngineInstanceID(engineOf(db))
	}

	OpenWithClientAndSigner = func(baseline fs.FS, gh github.Interface, signer func([]byte) (string, error)) (DB, error) {
		return NewOption(baseline).withClient(gh).CommitSigner(signer).Open()
	}

	OpenWithClientAndCommitter = func(baseline fs.FS, gh github.Interface, name, email string) (DB, error) {
		return NewOption(baseline).withClient(gh).Committer(name, email).Open()
	}
)
