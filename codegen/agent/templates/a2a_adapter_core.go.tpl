// Adapter implements the A2A service interface by routing requests to the agent runtime.
type Adapter struct {
	runtime agentruntime.Client
	agentID string
	baseURL string
	tasks   sync.Map
}

// taskState tracks the state of an active task.
type taskState struct {
	status string
	cancel context.CancelFunc
}

// NewAdapter creates a new A2A adapter for the {{ .Agent.GoName }} agent.
func NewAdapter(runtime agentruntime.Client, agentID, baseURL string) *Adapter {
	return &Adapter{
		runtime: runtime,
		agentID: agentID,
		baseURL: baseURL,
	}
}
