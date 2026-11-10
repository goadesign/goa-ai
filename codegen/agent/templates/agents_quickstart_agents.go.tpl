---

## 3. Meet Your Agents 🤖

Here are the detailed cheat sheets for each agent you designed.

{{- range .Services }}{{- range .Agents }}
<details>
<summary><strong>Agent: <code>{{ .Name }}</code></strong> (ID: <code>{{ .ID }}</code>)</summary>

* **Package:** `{{ .ImportPath }}`
* **Directory:** `{{ .Dir }}`
* **Config Struct:** `{{ .ConfigType }}`
* **Register Function:** `{{ .PackageNames.Register }}(ctx, rt, cfg)`
* **How to Run:**
    * **Sessions are first-class:** ask the service that owns sessions to create `sessionID` before starting a run, and supply a stable run ID for each workflow. The agent runtime receives a storage client but does not administer sessions.
    * **Synchronous (wait for result):**
        ```go
        client := {{ .PackageName }}.NewClient(rt)
        out, err := client.Run(ctx, sessionID, messages, runtime.WithRunID(runID))
        ```
    * **Asynchronous (get a handle):**
        ```go
        client := {{ .PackageName }}.NewClient(rt)
        handle, err := client.Start(ctx, sessionID, messages, runtime.WithRunID(runID))
        ```
* **Workflow Name:** `{{ .Runtime.Workflow.Name }}` (Queue: `{{ .Runtime.Workflow.Queue }}`)

#### Minimal Configuration

{{- $agent := . }}

```go
cfg := {{ .PackageName }}.{{ .ConfigType }}{
    Planner: myPlanner,
}
```
</details>
{{- end }}{{- end }}

