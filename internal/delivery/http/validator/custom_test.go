package validator

import (
	"testing"

	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
)

func TestValidateDecimalPositive(t *testing.T) {
	v := validator.New()
	err := RegisterCustomValidators(v)
	assert.NoError(t, err)

	type TestStruct struct {
		Amount string `validate:"decimal_positive"`
	}

	tests := []struct {
		name    string
		amount  string
		wantErr bool
	}{
		{
			name:    "positive decimal",
			amount:  "100.50",
			wantErr: false,
		},
		{
			name:    "negative decimal",
			amount:  "-100.50",
			wantErr: true,
		},
		{
			name:    "zero",
			amount:  "0",
			wantErr: true,
		},
		{
			name:    "invalid string",
			amount:  "abc",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testStruct := TestStruct{Amount: tt.amount}
			err := v.Struct(testStruct)
			if tt.wantErr {
				assert.Error(t, err, "Expected validation error for amount: %s", tt.amount)
			} else {
				assert.NoError(t, err, "Expected no validation error for amount: %s", tt.amount)
			}
		})
	}
}
