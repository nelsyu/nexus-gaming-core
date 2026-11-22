package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackofficeProviderID(t *testing.T) {
	for _, tenant := range []string{"TENANT_A", strings.Repeat("a", 39), strings.Repeat("站", 39)} {
		id, err := BackofficeProviderID(tenant)
		require.NoError(t, err)
		assert.Equal(t, "BACKOFFICE#"+tenant, id)
	}
	for _, tenant := range []string{"", "A:B", strings.Repeat("a", 40)} {
		_, err := BackofficeProviderID(tenant)
		assert.ErrorIs(t, err, ErrForbidden, tenant)
	}
}
