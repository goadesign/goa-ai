package gateway

import "errors"

// ErrProviderRequired indicates that a provider model.Client must be supplied.
var ErrProviderRequired = errors.New("model gateway: provider is required")
