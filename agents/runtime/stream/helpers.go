package stream

// Helper constructors to build typed events with the Base metadata populated.

// NewAssistantReply constructs an AssistantReply for the given run and text.
func NewAssistantReply(run, text string) AssistantReply {
    return AssistantReply{Base: Base{T: EventAssistantReply, R: run, P: text}, Text: text}
}

// NewPlannerThought constructs a PlannerThought for the given run and note.
func NewPlannerThought(run, note string) PlannerThought {
    return PlannerThought{Base: Base{T: EventPlannerThought, R: run, P: note}, Note: note}
}

// NewToolStart constructs a ToolStart for the given run and payload.
func NewToolStart(run string, payload ToolStartPayload) ToolStart {
    return ToolStart{Base: Base{T: EventToolStart, R: run, P: payload}, Data: payload}
}

// NewToolEnd constructs a ToolEnd for the given run and payload.
func NewToolEnd(run string, payload ToolEndPayload) ToolEnd {
    return ToolEnd{Base: Base{T: EventToolEnd, R: run, P: payload}, Data: payload}
}

