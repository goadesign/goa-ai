package lifecycle

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
)

func TestRegisteredRunTurnID(t *testing.T) {
	agent := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("assistant")}
	service := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "tools", Tool: "lookup.item"})}
	for _, tc := range []struct {
		name        string
		subject     genrunidentity.RunSubject
		runID       string
		predecessor string
		want        string
		invalid     bool
	}{
		{name: "initial agent", subject: agent, runID: "root"},
		{name: "agent successor", subject: agent, runID: "next", predecessor: "root", want: "next"},
		{name: "later agent successor", subject: agent, runID: "third", predecessor: "next", want: "third"},
		{name: "initial service", subject: service, runID: "root"},
		{name: "service successor", subject: service, runID: "next", predecessor: "root"},
		{name: "missing subject", runID: "root", invalid: true},
		{name: "missing run", subject: agent, invalid: true},
		{name: "self predecessor", subject: agent, runID: "root", predecessor: "root", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RegisteredRunTurnID(tc.subject, tc.runID, tc.predecessor)
			if tc.invalid {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
