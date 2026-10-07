// {{ .Fill }} decodes host answers into the authored service payload. Empty
// continuations remain present so the method can distinguish later input rounds.
func {{ .Fill }}(payload {{ .PayloadRef }}, input *{{ .Input.MCPPackage }}.CallContinuation) error {
 payload.{{ .Input.ContinuationField }} = nil
 if input != nil {
  requestState, inputResponses := input.RequestState,input.InputResponses
  if inputResponses == nil { inputResponses=make(map[string]json.RawMessage) }
  {{ .Input.ContinuationSource }}
 }
 return {{ .PayloadValidate }}(payload)
}

// {{ .Outcome }} validates the service's full result and returns either its
// completed value or the selected questions that suspend this invocation.
func {{ .Outcome }}(result {{ .ResultRef }}) ({{ .CompletedRef }}, *{{ .Input.MCPPackage }}.InputRequired, error) {
 var completed {{ .CompletedRef }}
 if err:= {{ .ResultValidate }}(result);err!=nil {return completed,nil,err}
 if pending,ok:= {{ .Input.OutcomeValue }}.AsInputRequired();ok {
  input,err:= {{ .Pending }}(pending)
  return completed,input,err
 }
 completed,_= {{ .Input.OutcomeValue }}.AsComplete()
 return completed,nil,nil
}

// {{ .Pending }} validates selected service questions and returns their host
// request contracts, retaining empty request objects and exact opaque state.
func {{ .Pending }}(pending {{ .Input.PendingRef }}) (*{{ .Input.MCPPackage }}.InputRequired,error) {
 {{ .Input.PendingSource }}
}
