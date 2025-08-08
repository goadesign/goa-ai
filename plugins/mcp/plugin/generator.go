package plugin

import (
	"bytes"
	"fmt"
	"sort"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

type Generator struct {
	genpkg string
	roots  []eval.Root
	files  []*codegen.File
}

type tool struct {
	Service string
	Method  string
	Desc    string
}

func NewGenerator(genpkg string, roots []eval.Root, files []*codegen.File) *Generator {
	return &Generator{genpkg: genpkg, roots: roots, files: files}
}

func (g *Generator) Run() ([]*codegen.File, error) {
	tools := collectTools()
	if len(tools) == 0 {
		return g.files, nil
	}
	var buf bytes.Buffer
	buf.WriteString("package server\n\n")
	buf.WriteString("import (\ncontext \"context\"\njson \"encoding/json\"\nfmt \"fmt\"\nos \"os\"\nbufio \"bufio\"\nstrings \"strings\"\n)\n\n")
	buf.WriteString("type rpcRequest struct{ JSONRPC string `json:\"jsonrpc\"` ID any `json:\"id\"` Method string `json:\"method\"` Params json.RawMessage `json:\"params\"`}\n")
	buf.WriteString("type rpcResponse struct{ JSONRPC string `json:\"jsonrpc\"` ID any `json:\"id\"` Result any `json:\"result,omitempty\"` Error *rpcError `json:\"error,omitempty\"`}\n")
	buf.WriteString("type rpcError struct{ Code int `json:\"code\"` Message string `json:\"message\"`}\n")
	buf.WriteString("\nfunc main(){ if err := run(); err!=nil { fmt.Fprintln(os.Stderr, err); os.Exit(1)}}\n")
	buf.WriteString("func run() error {\n")
	buf.WriteString("in := bufio.NewScanner(os.Stdin)\n")
	buf.WriteString("for in.Scan(){ line := strings.TrimSpace(in.Text()); if line==\"\" {continue}; var req rpcRequest; if err := json.Unmarshal([]byte(line), &req); err!=nil { continue } ; resp := handle(req); b,_ := json.Marshal(resp); fmt.Println(string(b)) }\n")
	buf.WriteString("return in.Err()}\n")
	buf.WriteString("\nfunc handle(req rpcRequest) rpcResponse {\nresp := rpcResponse{JSONRPC:\"2.0\", ID:req.ID}\nctx := context.Background()\n")

	sort.Slice(tools, func(i, j int) bool {
		if tools[i].Service == tools[j].Service {
			return tools[i].Method < tools[j].Method
		}
		return tools[i].Service < tools[j].Service
	})
	for _, t := range tools {
		buf.WriteString(fmt.Sprintf("if req.Method==\"%s.%s\" {\n\tvar p gen_%s_%s_Payload\n\t_ = json.Unmarshal(req.Params, &p)\n\tres, err := impl_%s_%s(ctx, &p)\n\tif err!=nil { resp.Error=&rpcError{Code:-32000, Message:err.Error()} } else { resp.Result = res }\n\treturn resp\n}\n", t.Service, t.Method, t.Service, codegen.Goify(t.Method, true), t.Service, codegen.Goify(t.Method, true)))
	}
	buf.WriteString("resp.Error=&rpcError{Code:-32601, Message:\"method not found\"}; return resp}\n")

	sf := &codegen.SectionTemplate{Name: "source", Source: buf.String()}
	file := &codegen.File{Path: "gen/mcp/server/server.go", SectionTemplates: []*codegen.SectionTemplate{sf}}
	g.files = append(g.files, file)
	return g.files, nil
}

func collectTools() []tool {
	var out []tool
	for _, s := range expr.Root.Services {
		for _, m := range s.Methods {
			if m.Meta != nil {
				if _, ok := m.Meta["mcp:tool"]; ok {
					d := ""
					if v, ok2 := m.Meta["mcp:description"]; ok2 && len(v) > 0 {
						d = v[0]
					}
					out = append(out, tool{Service: s.Name, Method: m.Name, Desc: d})
				}
			}
		}
	}
	return out
}