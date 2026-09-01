// Option configures a registry client.
type Option func(*Client)

{{- if .SecuritySchemes }}
{{- range .SecuritySchemes }}
{{- if isAPIKey .Kind }}

// {{ .AuthTypeName }} provides API key authentication.
type {{ .AuthTypeName }} struct {
	// Key is the API key value.
	Key string
}
{{- end }}
{{- if isOAuth2 .Kind }}

// {{ .AuthTypeName }} provides OAuth2 authentication.
type {{ .AuthTypeName }} struct {
	// Token is the OAuth2 access token.
	Token string
}
{{- end }}
{{- if isJWT .Kind }}

// {{ .AuthTypeName }} provides JWT authentication.
type {{ .AuthTypeName }} struct {
	// Token is the JWT token.
	Token string
}
{{- end }}
{{- if isBearer .Kind }}

// {{ .AuthTypeName }} provides bearer token authentication.
type {{ .AuthTypeName }} struct {
	// Token is the bearer token.
	Token string
}
{{- end }}
{{- if isBasicAuth .Kind }}

// {{ .AuthTypeName }} provides Basic authentication.
type {{ .AuthTypeName }} struct {
	// Username is the basic auth username.
	Username string
	// Password is the basic auth password.
	Password string
}
{{- end }}
{{- end }}
{{- end }}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		if client != nil {
			c.httpClient = client
		}
	}
}

// WithAuth sets the authentication provider.
func WithAuth(auth AuthProvider) Option {
	return func(c *Client) {
		c.auth = auth
	}
}

// WithTimeout sets the request timeout.
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		if timeout > 0 {
			c.timeout = timeout
		}
	}
}

// WithRetry configures retry behavior.
func WithRetry(maxRetries int, backoffBase time.Duration) Option {
	return func(c *Client) {
		if maxRetries >= 0 {
			c.retryMax = maxRetries
		}
		if backoffBase > 0 {
			c.retryBase = backoffBase
		}
	}
}

// WithEndpoint overrides the default registry endpoint.
func WithEndpoint(endpoint string) Option {
	return func(c *Client) {
		if endpoint != "" {
			c.endpoint = endpoint
		}
	}
}

{{- if .SecuritySchemes }}
{{- range .SecuritySchemes }}
{{- if isAPIKey .Kind }}

// {{ .OptionName }} creates an auth provider with the given API key.
func {{ .OptionName }}(key string) Option {
	return WithAuth(&{{ .AuthTypeName }}{Key: key})
}
{{- end }}
{{- if isOAuth2 .Kind }}

// {{ .OptionName }} creates an auth provider with the given OAuth2 token.
func {{ .OptionName }}(token string) Option {
	return WithAuth(&{{ .AuthTypeName }}{Token: token})
}
{{- end }}
{{- if isJWT .Kind }}

// {{ .OptionName }} creates an auth provider with the given JWT token.
func {{ .OptionName }}(token string) Option {
	return WithAuth(&{{ .AuthTypeName }}{Token: token})
}
{{- end }}
{{- if isBearer .Kind }}

// {{ .OptionName }} creates an auth provider with the given bearer token.
func {{ .OptionName }}(token string) Option {
	return WithAuth(&{{ .AuthTypeName }}{Token: token})
}
{{- end }}
{{- if isBasicAuth .Kind }}

// {{ .OptionName }} creates an auth provider with the given credentials.
func {{ .OptionName }}(username, password string) Option {
	return WithAuth(&{{ .AuthTypeName }}{
		Username: username,
		Password: password,
	})
}
{{- end }}
{{- end }}
{{- end }}
{{- if .SecuritySchemes }}
{{- range .SecuritySchemes }}
{{- if isAPIKey .Kind }}

// ApplyAuth implements AuthProvider.
func (a *{{ .AuthTypeName }}) ApplyAuth(req *http.Request) error {
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
{{- end }}
{{- if isOAuth2 .Kind }}

// ApplyAuth implements AuthProvider.
func (a *{{ .AuthTypeName }}) ApplyAuth(req *http.Request) error {
	if a.Token == "" {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	return nil
}
{{- end }}
{{- if isJWT .Kind }}

// ApplyAuth implements AuthProvider.
func (a *{{ .AuthTypeName }}) ApplyAuth(req *http.Request) error {
	if a.Token == "" {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	return nil
}
{{- end }}
{{- if isBearer .Kind }}

// ApplyAuth implements AuthProvider.
func (a *{{ .AuthTypeName }}) ApplyAuth(req *http.Request) error {
	if a.Token == "" {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	return nil
}
{{- end }}
{{- if isBasicAuth .Kind }}

// ApplyAuth implements AuthProvider.
func (a *{{ .AuthTypeName }}) ApplyAuth(req *http.Request) error {
	if a.Username == "" && a.Password == "" {
		return nil
	}
	req.SetBasicAuth(a.Username, a.Password)
	return nil
}
{{- end }}
{{- end }}
{{- end }}
