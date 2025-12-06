{{- if .HasSecuritySchemes }}
// A2AAuthProvider provides authentication credentials for A2A requests.
type A2AAuthProvider interface {
	// ApplyAuth adds authentication to the request.
	ApplyAuth(req *http.Request) error
}
{{- range .SecuritySchemes }}
{{- if eq .Type "http" }}
{{- if eq .Scheme "bearer" }}

// {{ goify .Name true }}Auth provides bearer token authentication for A2A requests.
type {{ goify .Name true }}Auth struct {
	// Token is the bearer token.
	Token string
}

// ApplyAuth implements A2AAuthProvider.
func (a *{{ goify .Name true }}Auth) ApplyAuth(req *http.Request) error {
	if a.Token == "" {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	return nil
}

// New{{ goify .Name true }}Auth creates a new bearer token auth provider.
func New{{ goify .Name true }}Auth(token string) *{{ goify .Name true }}Auth {
	return &{{ goify .Name true }}Auth{Token: token}
}
{{- else if eq .Scheme "basic" }}

// {{ goify .Name true }}Auth provides basic authentication for A2A requests.
type {{ goify .Name true }}Auth struct {
	// Username is the basic auth username.
	Username string
	// Password is the basic auth password.
	Password string
}

// ApplyAuth implements A2AAuthProvider.
func (a *{{ goify .Name true }}Auth) ApplyAuth(req *http.Request) error {
	if a.Username == "" && a.Password == "" {
		return nil
	}
	req.SetBasicAuth(a.Username, a.Password)
	return nil
}

// New{{ goify .Name true }}Auth creates a new basic auth provider.
func New{{ goify .Name true }}Auth(username, password string) *{{ goify .Name true }}Auth {
	return &{{ goify .Name true }}Auth{Username: username, Password: password}
}
{{- end }}
{{- end }}
{{- if eq .Type "apiKey" }}

// {{ goify .Name true }}Auth provides API key authentication for A2A requests.
type {{ goify .Name true }}Auth struct {
	// Key is the API key value.
	Key string
}

// ApplyAuth implements A2AAuthProvider.
func (a *{{ goify .Name true }}Auth) ApplyAuth(req *http.Request) error {
	if a.Key == "" {
		return nil
	}
	{{- if eq .In "header" }}
	req.Header.Set({{ printf "%q" .ParamName }}, a.Key)
	{{- else if eq .In "query" }}
	q := req.URL.Query()
	q.Set({{ printf "%q" .ParamName }}, a.Key)
	req.URL.RawQuery = q.Encode()
	{{- end }}
	return nil
}

// New{{ goify .Name true }}Auth creates a new API key auth provider.
func New{{ goify .Name true }}Auth(key string) *{{ goify .Name true }}Auth {
	return &{{ goify .Name true }}Auth{Key: key}
}
{{- end }}
{{- if eq .Type "oauth2" }}

// {{ goify .Name true }}Auth provides OAuth2 authentication for A2A requests.
type {{ goify .Name true }}Auth struct {
	// Token is the OAuth2 access token.
	Token string
}

// ApplyAuth implements A2AAuthProvider.
func (a *{{ goify .Name true }}Auth) ApplyAuth(req *http.Request) error {
	if a.Token == "" {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	return nil
}

// New{{ goify .Name true }}Auth creates a new OAuth2 auth provider.
func New{{ goify .Name true }}Auth(token string) *{{ goify .Name true }}Auth {
	return &{{ goify .Name true }}Auth{Token: token}
}
{{- end }}
{{- end }}
{{- end }}
