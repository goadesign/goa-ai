// Package mcp reads HTTP authentication challenges before OAuth discovery. The
// parser follows HTTP token and quoted-string rules, keeps separate challenges
// separate, and returns no server-authored header text in errors.
package mcp

import (
	"errors"
	"net/url"
	"strings"
)

type (
	// oauthChallenge retains one Bearer challenge's discovery and scope inputs.
	// Values from different challenges are never combined into a grant request.
	oauthChallenge struct {
		metadata *url.URL
		scopes   []string
		error    string
	}
	// authenticationChallenge holds one scheme and its parsed HTTP parameters.
	authenticationChallenge struct {
		scheme string
		params map[string]string
		token  bool
	}
	// challengeReader reads the remaining bytes of one HTTP header field.
	challengeReader struct {
		remaining string
	}
)

// oauthChallenges accepts multiple HTTP fields and comma-separated challenges.
// It validates every challenge's syntax, then returns only Bearer challenges;
// unrelated schemes and unknown parameters cannot supply OAuth grant inputs.
func oauthChallenges(fields []string) ([]oauthChallenge, error) {
	var result []oauthChallenge
	for _, field := range fields {
		reader := challengeReader{remaining: field}
		for reader.listItem() {
			challenge, err := reader.challenge()
			if err != nil {
				return nil, errors.New("mcp: malformed authentication challenge")
			}
			if !strings.EqualFold(challenge.scheme, "Bearer") {
				continue
			}
			if challenge.token {
				return nil, errors.New("mcp: Bearer challenge requires authentication parameters")
			}
			value := oauthChallenge{error: challenge.params["error"]}
			if address, present := challenge.params["resource_metadata"]; present {
				value.metadata, err = authorizationURL(address, false)
				if err != nil {
					return nil, errors.New("mcp: challenge resource metadata must identify an exact HTTPS URL")
				}
			}
			if scope, present := challenge.params["scope"]; present {
				value.scopes = strings.Split(scope, " ")
				for _, token := range value.scopes {
					if !validScopeToken(token) {
						return nil, errors.New("mcp: authentication challenge has invalid OAuth scopes")
					}
				}
			}
			for _, name := range []string{"error", "error_description"} {
				for _, character := range challenge.params[name] {
					if character < 0x20 || character > 0x7e || character == '"' || character == '\\' {
						return nil, errors.New("mcp: authentication challenge has an invalid OAuth error parameter")
					}
				}
			}
			result = append(result, value)
		}
	}
	return result, nil
}

// challenge consumes one scheme and either its parameters or token68 value.
// A comma starts another challenge only when the next token is not a parameter.
func (r *challengeReader) challenge() (authenticationChallenge, error) {
	value := authenticationChallenge{scheme: r.token(), params: make(map[string]string)}
	if value.scheme == "" {
		return value, errors.New("authentication scheme is missing")
	}
	if r.remaining == "" || r.remaining[0] == ',' {
		return value, nil
	}
	if r.remaining[0] != ' ' {
		return value, errors.New("scheme must be followed by spaces")
	}
	r.remaining = strings.TrimLeft(r.remaining, " \t")
	if r.remaining == "" || r.remaining[0] == ',' {
		return value, nil
	}
	// token68 can contain '=' padding; it is different from a named parameter.
	end := strings.IndexAny(r.remaining, ", \t")
	if end == -1 {
		end = len(r.remaining)
	}
	lookahead := *r
	lookahead.token()
	lookahead.whitespace()
	parameter := strings.HasPrefix(lookahead.remaining, "=")
	if authenticationToken68(r.remaining[:end]) && (!parameter || strings.Contains(r.remaining[:end], "=")) {
		value.token = true
		r.remaining = r.remaining[end:]
		r.whitespace()
		if r.remaining != "" && r.remaining[0] != ',' {
			return value, errors.New("unexpected bytes after token68")
		}
		return value, nil
	}
	for {
		name := strings.ToLower(r.token())
		r.whitespace()
		if name == "" || r.remaining == "" || r.remaining[0] != '=' {
			return value, errors.New("authentication parameter requires a value")
		}
		r.remaining = r.remaining[1:]
		r.whitespace()
		parameter, err := r.parameter()
		if err != nil {
			return value, err
		}
		if _, exists := value.params[name]; exists {
			return value, errors.New("duplicate authentication parameter")
		}
		value.params[name] = parameter
		r.whitespace()
		if r.remaining == "" {
			return value, nil
		}
		if r.remaining[0] != ',' {
			return value, errors.New("authentication parameters require commas")
		}
		next := *r
		if !next.listItem() {
			*r = next
			return value, nil
		}
		name = next.token()
		next.whitespace()
		if name == "" || next.remaining == "" || next.remaining[0] != '=' {
			return value, nil
		}
		r.listItem()
	}
}

// parameter decodes one HTTP token or quoted string, including quoted commas
// and escaped bytes. Unterminated strings and control bytes fail at this boundary.
func (r *challengeReader) parameter() (string, error) {
	if r.remaining == "" || r.remaining[0] != '"' {
		value := r.token()
		if value == "" {
			return "", errors.New("authentication parameter is empty")
		}
		return value, nil
	}
	r.remaining = r.remaining[1:]
	var value strings.Builder
	for len(r.remaining) > 0 {
		character := r.remaining[0]
		r.remaining = r.remaining[1:]
		switch character {
		case '"':
			return value.String(), nil
		case '\\':
			if r.remaining == "" {
				return "", errors.New("quoted escape is incomplete")
			}
			character = r.remaining[0]
			r.remaining = r.remaining[1:]
		}
		if (character < 0x20 && character != '\t') || character == 0x7f {
			return "", errors.New("quoted parameter has a control byte")
		}
		value.WriteByte(character)
	}
	return "", errors.New("quoted parameter is unterminated")
}

// listItem consumes HTTP list whitespace and empty members. The caller receives
// whether another nonempty challenge or parameter begins at the remaining text.
func (r *challengeReader) listItem() bool {
	r.remaining = strings.TrimLeft(r.remaining, " \t,")
	return r.remaining != ""
}

// token consumes the ASCII characters permitted in an HTTP token.
func (r *challengeReader) token() string {
	end := 0
	for end < len(r.remaining) && authenticationTokenByte(r.remaining[end]) {
		end++
	}
	value := r.remaining[:end]
	r.remaining = r.remaining[end:]
	return value
}

// whitespace consumes the optional spaces and tabs around HTTP parameters.
func (r *challengeReader) whitespace() {
	r.remaining = strings.TrimLeft(r.remaining, " \t")
}

// authenticationToken68 identifies an opaque challenge token and its trailing
// equals-sign padding without mistaking name=value for a token.
func authenticationToken68(value string) bool {
	if value == "" {
		return false
	}
	padding := false
	for index, character := range value {
		if character == '=' && index > 0 {
			padding = true
			continue
		}
		if padding || ((character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && !strings.ContainsRune("-._~+/", character)) {
			return false
		}
	}
	return true
}

// authenticationTokenByte follows RFC 9110's token grammar for scheme and names.
func authenticationTokenByte(character byte) bool {
	return (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
		(character >= '0' && character <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character))
}
