package runtime

// Workflow fixtures must return committed record IDs from the selected start
// command, including the distinct sessionless child command.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/session"
)

func TestStorageResultStartVariants(t *testing.T) {
	started := &api.RecordActivityInput{EventKey: "started"}
	linked := &api.RecordActivityInput{EventKey: "linked"}
	for _, test := range []struct {
		name    string
		command *api.StorageActivityCommand
		result  func(*api.StorageActivityResult) *api.StartRunResult
		ids     []string
	}{
		{"root", &api.StorageActivityCommand{RootStart: &api.RootRunStartCommand{Started: started}},
			func(out *api.StorageActivityResult) *api.StartRunResult { return out.RootStart }, []string{"started"}},
		{"child", &api.StorageActivityCommand{ChildStart: &api.ChildRunStartCommand{ParentLinked: linked, Started: started}},
			func(out *api.StorageActivityResult) *api.StartRunResult { return out.ChildStart }, []string{"linked", "started"}},
		{"one shot", &api.StorageActivityCommand{OneShotStart: &api.OneShotRunStartCommand{Started: started}},
			func(out *api.StorageActivityResult) *api.StartRunResult { return out.OneShotStart }, []string{"started"}},
		{"one shot child", &api.StorageActivityCommand{OneShotChildStart: &api.OneShotChildRunStartCommand{ParentLinked: linked, Started: started}},
			func(out *api.StorageActivityResult) *api.StartRunResult { return out.OneShotChildStart }, []string{"linked", "started"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := test.result(testStorageResult(test.command))
			require.NotNil(t, result)
			require.Equal(t, session.RunStartProceed, result.Outcome)
			require.Equal(t, session.RunStatusRunning, result.RunStatus)
			ids := make([]string, 0, len(result.Records))
			for _, record := range result.Records {
				ids = append(ids, record.ID)
			}
			require.Equal(t, test.ids, ids)
		})
	}
}
