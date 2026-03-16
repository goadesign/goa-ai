package mcp

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCallerOptionsDoNotExposeDeadProtocolVersionFields(t *testing.T) {
	t.Parallel()

	assert.False(t, hasField[HTTPOptions]("ProtocolVersion"))
	assert.False(t, hasField[StdioOptions]("ProtocolVersion"))
}

func hasField[T any](name string) bool {
	_, ok := reflect.TypeFor[T]().FieldByName(name)
	return ok
}
