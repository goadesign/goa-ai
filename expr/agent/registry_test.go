package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	goaexpr "goa.design/goa/v3/expr"
)

func TestRegistryExpr_EvalName(t *testing.T) {
	r := &RegistryExpr{Name: "test-registry"}
	require.Equal(t, `registry "test-registry"`, r.EvalName())
}

func TestRegistryExpr_AddSecurityRequirement(t *testing.T) {
	r := &RegistryExpr{Name: "test"}
	sec := &goaexpr.SecurityExpr{
		Schemes: []*goaexpr.SchemeExpr{{SchemeName: "api_key"}},
	}

	r.AddSecurityRequirement(sec)

	require.Len(t, r.Requirements, 1)
	require.Equal(t, "api_key", r.Requirements[0].Schemes[0].SchemeName)
}

func TestRegistryExpr_SetURL(t *testing.T) {
	r := &RegistryExpr{Name: "test"}

	r.SetURL("https://registry.example.com")

	require.Equal(t, "https://registry.example.com", r.URL)
}

func TestRegistryExpr_Prepare(t *testing.T) {
	t.Run("sets default APIVersion when empty", func(t *testing.T) {
		r := &RegistryExpr{Name: "test"}
		r.Prepare()
		require.Equal(t, "v1", r.APIVersion)
	})

	t.Run("preserves existing APIVersion", func(t *testing.T) {
		r := &RegistryExpr{Name: "test", APIVersion: "v2"}
		r.Prepare()
		require.Equal(t, "v2", r.APIVersion)
	})
}

func TestRegistryExpr_Validate(t *testing.T) {
	tests := []struct {
		name    string
		reg     *RegistryExpr
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid registry",
			reg:     &RegistryExpr{Name: "test", URL: "https://registry.example.com"},
			wantErr: false,
		},
		{
			name:    "missing name",
			reg:     &RegistryExpr{URL: "https://registry.example.com"},
			wantErr: true,
			errMsg:  "registry name is required",
		},
		{
			name:    "missing URL",
			reg:     &RegistryExpr{Name: "test"},
			wantErr: true,
			errMsg:  "registry URL is required",
		},
		{
			name:    "invalid URL",
			reg:     &RegistryExpr{Name: "test", URL: "://invalid"},
			wantErr: true,
			errMsg:  "invalid registry URL",
		},
		{
			name:    "negative SyncInterval",
			reg:     &RegistryExpr{Name: "test", URL: "https://example.com", SyncInterval: -1 * time.Second},
			wantErr: true,
			errMsg:  "SyncInterval must be non-negative",
		},
		{
			name:    "negative CacheTTL",
			reg:     &RegistryExpr{Name: "test", URL: "https://example.com", CacheTTL: -1 * time.Second},
			wantErr: true,
			errMsg:  "CacheTTL must be non-negative",
		},
		{
			name:    "negative Timeout",
			reg:     &RegistryExpr{Name: "test", URL: "https://example.com", Timeout: -1 * time.Second},
			wantErr: true,
			errMsg:  "Timeout must be non-negative",
		},
		{
			name: "negative RetryPolicy.MaxRetries",
			reg: &RegistryExpr{
				Name: "test",
				URL:  "https://example.com",
				RetryPolicy: &RetryPolicyExpr{
					MaxRetries: -1,
				},
			},
			wantErr: true,
			errMsg:  "RetryPolicy.MaxRetries must be non-negative",
		},
		{
			name: "negative RetryPolicy.BackoffBase",
			reg: &RegistryExpr{
				Name: "test",
				URL:  "https://example.com",
				RetryPolicy: &RetryPolicyExpr{
					BackoffBase: -1 * time.Second,
				},
			},
			wantErr: true,
			errMsg:  "RetryPolicy.BackoffBase must be non-negative",
		},
		{
			name: "negative RetryPolicy.BackoffMax",
			reg: &RegistryExpr{
				Name: "test",
				URL:  "https://example.com",
				RetryPolicy: &RetryPolicyExpr{
					BackoffMax: -1 * time.Second,
				},
			},
			wantErr: true,
			errMsg:  "RetryPolicy.BackoffMax must be non-negative",
		},
		{
			name: "BackoffBase exceeds BackoffMax",
			reg: &RegistryExpr{
				Name: "test",
				URL:  "https://example.com",
				RetryPolicy: &RetryPolicyExpr{
					BackoffBase: 10 * time.Second,
					BackoffMax:  5 * time.Second,
				},
			},
			wantErr: true,
			errMsg:  "RetryPolicy.BackoffBase must not exceed BackoffMax",
		},
		{
			name: "valid RetryPolicy",
			reg: &RegistryExpr{
				Name: "test",
				URL:  "https://example.com",
				RetryPolicy: &RetryPolicyExpr{
					MaxRetries:  3,
					BackoffBase: time.Second,
					BackoffMax:  10 * time.Second,
				},
			},
			wantErr: false,
		},
		{
			name: "valid durations",
			reg: &RegistryExpr{
				Name:         "test",
				URL:          "https://example.com",
				SyncInterval: 5 * time.Minute,
				CacheTTL:     time.Hour,
				Timeout:      30 * time.Second,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.reg.Validate()
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
