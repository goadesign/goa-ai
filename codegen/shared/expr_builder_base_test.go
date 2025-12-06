package shared

import (
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"goa.design/goa/v3/expr"
)

// TestNewProtocolExprBuilderBase verifies that a new builder is properly initialized.
// **Validates: Requirements 10.1**
func TestNewProtocolExprBuilderBase(t *testing.T) {
	builder := NewProtocolExprBuilderBase()

	if builder == nil {
		t.Fatal("NewProtocolExprBuilderBase returned nil")
	}
	if builder.types == nil {
		t.Error("types map should be initialized")
	}
	if len(builder.types) != 0 {
		t.Error("types map should be empty initially")
	}
}

// TestGetOrCreateType verifies the GetOrCreateType method.
// **Validates: Requirements 10.1**
func TestGetOrCreateType(t *testing.T) {
	t.Run("creates new type when not exists", func(t *testing.T) {
		builder := NewProtocolExprBuilderBase()
		callCount := 0

		result := builder.GetOrCreateType("TestType", func() *expr.AttributeExpr {
			callCount++
			return &expr.AttributeExpr{Type: expr.String}
		})

		if result == nil {
			t.Fatal("GetOrCreateType returned nil")
		}
		if result.TypeName != "TestType" {
			t.Errorf("expected TypeName 'TestType', got %q", result.TypeName)
		}
		if callCount != 1 {
			t.Errorf("builder function should be called once, was called %d times", callCount)
		}
	})

	t.Run("returns existing type without calling builder", func(t *testing.T) {
		builder := NewProtocolExprBuilderBase()
		callCount := 0

		// First call creates the type
		first := builder.GetOrCreateType("TestType", func() *expr.AttributeExpr {
			callCount++
			return &expr.AttributeExpr{Type: expr.String}
		})

		// Second call should return the same type without calling builder
		second := builder.GetOrCreateType("TestType", func() *expr.AttributeExpr {
			callCount++
			return &expr.AttributeExpr{Type: expr.Int}
		})

		if first != second {
			t.Error("GetOrCreateType should return the same instance for the same name")
		}
		if callCount != 1 {
			t.Errorf("builder function should only be called once, was called %d times", callCount)
		}
	})

	t.Run("creates different types for different names", func(t *testing.T) {
		builder := NewProtocolExprBuilderBase()

		type1 := builder.GetOrCreateType("Type1", func() *expr.AttributeExpr {
			return &expr.AttributeExpr{Type: expr.String}
		})
		type2 := builder.GetOrCreateType("Type2", func() *expr.AttributeExpr {
			return &expr.AttributeExpr{Type: expr.Int}
		})

		if type1 == type2 {
			t.Error("different names should create different types")
		}
		if type1.TypeName != "Type1" {
			t.Errorf("expected TypeName 'Type1', got %q", type1.TypeName)
		}
		if type2.TypeName != "Type2" {
			t.Errorf("expected TypeName 'Type2', got %q", type2.TypeName)
		}
	})
}

// TestUserTypeAttr verifies the UserTypeAttr method.
// **Validates: Requirements 10.1**
func TestUserTypeAttr(t *testing.T) {
	builder := NewProtocolExprBuilderBase()

	attr := builder.UserTypeAttr("TestType", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{Type: expr.String}
	})

	if attr == nil {
		t.Fatal("UserTypeAttr returned nil")
	}

	ut, ok := attr.Type.(*expr.UserTypeExpr)
	if !ok {
		t.Fatalf("expected Type to be *expr.UserTypeExpr, got %T", attr.Type)
	}
	if ut.TypeName != "TestType" {
		t.Errorf("expected TypeName 'TestType', got %q", ut.TypeName)
	}
}

// TestCollectUserTypes verifies the CollectUserTypes method.
// **Validates: Requirements 10.1**
func TestCollectUserTypes(t *testing.T) {
	t.Run("returns empty slice for empty builder", func(t *testing.T) {
		builder := NewProtocolExprBuilderBase()
		types := builder.CollectUserTypes()

		if types == nil {
			t.Error("CollectUserTypes should return non-nil slice")
		}
		if len(types) != 0 {
			t.Errorf("expected empty slice, got %d elements", len(types))
		}
	})

	t.Run("returns all registered types", func(t *testing.T) {
		builder := NewProtocolExprBuilderBase()

		builder.GetOrCreateType("Alpha", func() *expr.AttributeExpr {
			return &expr.AttributeExpr{Type: expr.String}
		})
		builder.GetOrCreateType("Beta", func() *expr.AttributeExpr {
			return &expr.AttributeExpr{Type: expr.Int}
		})
		builder.GetOrCreateType("Gamma", func() *expr.AttributeExpr {
			return &expr.AttributeExpr{Type: expr.Boolean}
		})

		types := builder.CollectUserTypes()

		if len(types) != 3 {
			t.Fatalf("expected 3 types, got %d", len(types))
		}

		// Verify alphabetical order
		names := make([]string, len(types))
		for i, ut := range types {
			names[i] = ut.(*expr.UserTypeExpr).TypeName
		}
		if names[0] != "Alpha" || names[1] != "Beta" || names[2] != "Gamma" {
			t.Errorf("expected [Alpha, Beta, Gamma], got %v", names)
		}
	})

	t.Run("deduplicates types with same name", func(t *testing.T) {
		builder := NewProtocolExprBuilderBase()

		builder.GetOrCreateType("Same", func() *expr.AttributeExpr {
			return &expr.AttributeExpr{Type: expr.String}
		})
		builder.GetOrCreateType("Same", func() *expr.AttributeExpr {
			return &expr.AttributeExpr{Type: expr.Int}
		})

		types := builder.CollectUserTypes()

		if len(types) != 1 {
			t.Errorf("expected 1 type (deduplicated), got %d", len(types))
		}
	})
}

// TestTypes verifies the Types accessor method.
// **Validates: Requirements 10.1**
func TestTypes(t *testing.T) {
	builder := NewProtocolExprBuilderBase()

	builder.GetOrCreateType("TestType", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{Type: expr.String}
	})

	types := builder.Types()

	if types == nil {
		t.Fatal("Types() returned nil")
	}
	if len(types) != 1 {
		t.Errorf("expected 1 type, got %d", len(types))
	}
	if _, ok := types["TestType"]; !ok {
		t.Error("expected 'TestType' in types map")
	}
}

// TestPrepareAndValidate verifies the PrepareAndValidate method.
// Note: Full integration testing of PrepareAndValidate is done through the
// A2A and MCP expression builders which construct complete root expressions.
// These unit tests focus on the method's contract: restoring global state.
// **Validates: Requirements 10.1**
func TestPrepareAndValidate(t *testing.T) {
	t.Run("restores original expr.Root after execution", func(t *testing.T) {
		builder := NewProtocolExprBuilderBase()
		originalRoot := expr.Root

		// Create a minimal root - validation may fail but that's OK
		// The key behavior we're testing is that expr.Root is restored
		service := &expr.ServiceExpr{
			Name:    "TestService",
			Methods: []*expr.MethodExpr{},
		}

		httpExpr := &expr.HTTPExpr{
			Services: []*expr.HTTPServiceExpr{},
		}

		root := &expr.RootExpr{
			Services: []*expr.ServiceExpr{service},
			API: &expr.APIExpr{
				Name: "TestAPI",
				HTTP: httpExpr,
				GRPC: &expr.GRPCExpr{Services: []*expr.GRPCServiceExpr{}},
			},
		}

		// Use defer/recover to handle any panics and still check restoration
		func() {
			defer func() {
				// Recover from any panic - we just want to verify restoration
				_ = recover()
			}()
			_ = builder.PrepareAndValidate(root)
		}()

		if expr.Root != originalRoot {
			t.Error("PrepareAndValidate should restore original expr.Root")
		}
	})
}

// TestDeterministicUserTypeCollection verifies Property 1: Deterministic User Type Collection.
// **Feature: a2a-codegen-refactor, Property 1: Deterministic User Type Collection**
// *For any* set of user types registered with an expression builder, collecting them
// should always produce the same ordered list regardless of insertion order.
// **Validates: Requirements 1.5**
func TestDeterministicUserTypeCollection(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("user type collection is deterministic regardless of insertion order", prop.ForAll(
		func(typeNames []string) bool {
			// Create two builders and insert types in different orders
			builder1 := NewProtocolExprBuilderBase()
			builder2 := NewProtocolExprBuilderBase()

			// Insert in original order for builder1
			for _, name := range typeNames {
				builder1.GetOrCreateType(name, func() *expr.AttributeExpr {
					return &expr.AttributeExpr{Type: expr.String}
				})
			}

			// Insert in reverse order for builder2
			for i := len(typeNames) - 1; i >= 0; i-- {
				builder2.GetOrCreateType(typeNames[i], func() *expr.AttributeExpr {
					return &expr.AttributeExpr{Type: expr.String}
				})
			}

			// Collect types from both builders
			types1 := builder1.CollectUserTypes()
			types2 := builder2.CollectUserTypes()

			// Both should have the same length
			if len(types1) != len(types2) {
				return false
			}

			// Both should produce the same order
			for i := range types1 {
				ut1, ok1 := types1[i].(*expr.UserTypeExpr)
				ut2, ok2 := types2[i].(*expr.UserTypeExpr)
				if !ok1 || !ok2 {
					return false
				}
				if ut1.TypeName != ut2.TypeName {
					return false
				}
			}

			return true
		},
		genUniqueTypeNames(),
	))

	properties.TestingRun(t)
}

// TestCollectUserTypesAlphabeticalOrder verifies that types are sorted alphabetically.
// **Feature: a2a-codegen-refactor, Property 1: Deterministic User Type Collection**
// **Validates: Requirements 1.5**
func TestCollectUserTypesAlphabeticalOrder(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("collected types are in alphabetical order", prop.ForAll(
		func(typeNames []string) bool {
			builder := NewProtocolExprBuilderBase()

			for _, name := range typeNames {
				builder.GetOrCreateType(name, func() *expr.AttributeExpr {
					return &expr.AttributeExpr{Type: expr.String}
				})
			}

			types := builder.CollectUserTypes()

			// Verify alphabetical order
			for i := 1; i < len(types); i++ {
				prev, ok1 := types[i-1].(*expr.UserTypeExpr)
				curr, ok2 := types[i].(*expr.UserTypeExpr)
				if !ok1 || !ok2 {
					return false
				}
				if prev.TypeName > curr.TypeName {
					return false
				}
			}

			return true
		},
		genUniqueTypeNames(),
	))

	properties.TestingRun(t)
}

// genUniqueTypeNames generates a slice of unique non-empty type names.
func genUniqueTypeNames() gopter.Gen {
	return gen.SliceOfN(10, gen.AlphaString()).
		SuchThat(func(names []string) bool {
			seen := make(map[string]bool)
			for _, n := range names {
				if n == "" || seen[n] {
					return false
				}
				seen[n] = true
			}
			return len(names) >= 2
		})
}
