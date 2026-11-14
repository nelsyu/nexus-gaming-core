package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToMinorUnits(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    int64
		wantErr bool
	}{
		{name: "integer", in: "100", want: 1_000_000},
		{name: "smallest unit", in: "0.0001", want: 1},
		{name: "float trap value 0.3", in: "0.3", want: 3_000},
		{name: "trailing zeros beyond scale are fine", in: "1.50000", want: 15_000},
		{name: "five decimals rejected", in: "0.00001", wantErr: true},
		{name: "overflow int64 rejected", in: "99999999999999999999", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ToMinorUnits(decimal.RequireFromString(tt.in))
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidAmount)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
