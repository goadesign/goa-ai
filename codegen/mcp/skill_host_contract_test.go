// These checks compile the reference host with a normally generated Skills
// server and client. The host owns origin labels, consent and model context;
// generated contracts and the shared verifier own protocol and file validation.
package codegen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPSkillHostHTTP(t *testing.T) {
	sources := make(map[string]string)
	for _, name := range []string{"design.go", "host.go", "host_test.go"} {
		// #nosec G304 -- these three fixed fixture names come from this test.
		data, err := os.ReadFile(filepath.Join("testdata", "skills_host", name))
		require.NoError(t, err)
		sources[name] = string(data)
	}
	runMCPPeer(t, "skill-host.local", sources["design.go"], sources["host_test.go"], map[string]string{"host.go": sources["host.go"]})
}
