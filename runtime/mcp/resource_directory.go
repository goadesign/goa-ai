// Package mcp checks directory observations at the protocol boundary. A directory
// reply contains direct children under the same resource authority, never a
// recursive listing. URI authorities identify resources and are never resolved.
package mcp

import (
	"errors"
	"net/url"
	"strings"
)

// ValidateResourceDirectory checks a requested directory URI and the resource
// URIs returned in one page. Empty pages are valid. Trailing slashes, unsafe path
// segments, duplicate children, other authorities and deeper descendants fail.
// Acceptance grants no permission to read files or extend a held Skill manifest.
func ValidateResourceDirectory(directory string, children []string) error {
	parent, err := resourceAddress(directory)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(children))
	for _, uri := range children {
		if _, duplicate := seen[uri]; duplicate {
			return errors.New("mcp: resource directory contains a duplicate URI")
		}
		seen[uri] = struct{}{}
		child, err := resourceAddress(uri)
		if err != nil {
			return err
		}
		prefix := parent.Path + "/"
		if child.Scheme != parent.Scheme || child.Host != parent.Host || child.User.String() != parent.User.String() || !strings.HasPrefix(child.Path, prefix) || strings.Contains(strings.TrimPrefix(child.Path, prefix), "/") {
			return errors.New("mcp: resource directory must return only direct children")
		}
	}
	return nil
}

// resourceAddress accepts hierarchical resource identifiers without
// normalizing their paths. The decoded segments cannot change directory levels
// or become local separators if a host later materializes verified content.
func resourceAddress(uri string) (*url.URL, error) {
	address, err := url.Parse(uri)
	if err != nil || address.Scheme == "" || address.Opaque != "" || (address.Host == "" && address.Path == "") || (address.Path != "" && !strings.HasPrefix(address.Path, "/")) || strings.HasSuffix(address.Path, "/") {
		return nil, errors.New("mcp: resource requires an absolute hierarchical URI without a trailing slash")
	}
	for _, segment := range strings.Split(address.Path, "/") {
		if segment == "." || segment == ".." || strings.ContainsAny(segment, "\\\x00") {
			return nil, errors.New("mcp: resource path contains an unsafe segment")
		}
	}
	return address, nil
}
