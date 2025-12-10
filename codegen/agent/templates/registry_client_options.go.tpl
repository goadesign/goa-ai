// Option configures a registry client.
type Option func(*Client)

{{- if .SecuritySchemes }}
{{- range .SecuritySchemes }}
{{- if isAPIKey .Kind }}

// {{ goify .Name true }}Auth provides API key authentication.
type {{ goify .Name true }}Auth struct {
	// Key is the API key value.
	Key string
}
{{- end }}
{{- if isOAuth2 .Kind }}

// {{ goify .Name true }}Auth provides OAuth2 authentication.
type {{ goify .Name true }}Auth struct {
	// Token is the OAuth2 access token.
	Token string
}
{{- end }}
{{- if isJWT .Kind }}

// {{ goify .Name true }}Auth provides JWT authentication.
type {{ goify .Name true }}Auth struct {
	// Token is the JWT token.
	Token string
}
{{- end }}
{{- if isBasicAuth .Kind }}

// {{ goify .Name true }}Auth provides Basic authentication.
type {{ goify .Name true }}Auth struct {
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

// With{{ goify .Name true }} creates an auth provider with the given API key.
func With{{ goify .Name true }}(key string) Option {
	return WithAuth(&{{ goify .Name true }}Auth{Key: key})
}
{{- end }}
{{- if isOAuth2 .Kind }}

// With{{ goify .Name true }} creates an auth provider with the given OAuth2 token.
func With{{ goify .Name true }}(token string) Option {
	return WithAuth(&{{ goify .Name true }}Auth{Token: token})
}
{{- end }}
{{- if isJWT .Kind }}

// With{{ goify .Name true }} creates an auth provider with the given JWT token.
func With{{ goify .Name true }}(token string) Option {
	return WithAuth(&{{ goify .Name true }}Auth{Token: token})
}
{{- end }}
{{- if isBasicAuth .Kind }}

// With{{ goify .Name true }} creates an auth provider with the given credentials.
func With{{ goify .Name true }}(username, password string) Option {
	return WithAuth(&{{ goify .Name true }}Auth{
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
{{- end }}
{{- if isOAuth2 .Kind }}

// ApplyAuth implements AuthProvider.
func (a *{{ goify .Name true }}Auth) ApplyAuth(req *http.Request) error {
	if a.Token == "" {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	return nil
}
{{- end }}
{{- if isJWT .Kind }}

// ApplyAuth implements AuthProvider.
func (a *{{ goify .Name true }}Auth) ApplyAuth(req *http.Request) error {
	if a.Token == "" {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	return nil
}
{{- end }}
{{- if isBasicAuth .Kind }}

// ApplyAuth implements AuthProvider.
func (a *{{ goify .Name true }}Auth) ApplyAuth(req *http.Request) error {
	if a.Username == "" && a.Password == "" {
		return nil
	}
	req.SetBasicAuth(a.Username, a.Password)
	return nil
}
{{- end }}
{{- end }}
{{- end }}
