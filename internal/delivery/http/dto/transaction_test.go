package dto

import (
	"testing"

	customvalidator "github.com/bosstest/nexus-core/internal/delivery/http/validator"
	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
)

func TestTransactionRequest_Validation(t *testing.T) {
	// 初始化自定義驗證器
	v := validator.New()
	err := customvalidator.RegisterCustomValidators(v)
	assert.NoError(t, err)

	tests := []struct {
		name    string
		req     TransactionRequest
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid BET transaction",
			req: TransactionRequest{
				UserID:       1,
				Currency:     "TWD",
				Type:         domain.TxTypeBet,
				Amount:       "100.50",
				ProviderID:   "PG_SOFT",
				ProviderTxID: "bet_12345",
				ReferenceID:  "round_001",
			},
			wantErr: false,
		},
		{
			name: "invalid - negative amount",
			req: TransactionRequest{
				UserID:       1,
				Currency:     "TWD",
				Type:         domain.TxTypeBet,
				Amount:       "-100.50",
				ProviderID:   "PG_SOFT",
				ProviderTxID: "bet_12345",
			},
			wantErr: true,
			errMsg:  "Amount",
		},
		{
			name: "invalid - zero amount",
			req: TransactionRequest{
				UserID:       1,
				Currency:     "TWD",
				Type:         domain.TxTypeBet,
				Amount:       "0",
				ProviderID:   "PG_SOFT",
				ProviderTxID: "bet_12345",
			},
			wantErr: true,
			errMsg:  "Amount",
		},
		{
			name: "invalid - non-decimal amount",
			req: TransactionRequest{
				UserID:       1,
				Currency:     "TWD",
				Type:         domain.TxTypeBet,
				Amount:       "abc",
				ProviderID:   "PG_SOFT",
				ProviderTxID: "bet_12345",
			},
			wantErr: true,
			errMsg:  "Amount",
		},
		{
			name: "invalid - currency not 3 chars",
			req: TransactionRequest{
				UserID:       1,
				Currency:     "TW",
				Type:         domain.TxTypeBet,
				Amount:       "100.50",
				ProviderID:   "PG_SOFT",
				ProviderTxID: "bet_12345",
			},
			wantErr: true,
			errMsg:  "Currency",
		},
		{
			name: "invalid - currency not uppercase",
			req: TransactionRequest{
				UserID:       1,
				Currency:     "twd",
				Type:         domain.TxTypeBet,
				Amount:       "100.50",
				ProviderID:   "PG_SOFT",
				ProviderTxID: "bet_12345",
			},
			wantErr: true,
			errMsg:  "Currency",
		},
		{
			name: "invalid - unknown transaction type",
			req: TransactionRequest{
				UserID:       1,
				Currency:     "TWD",
				Type:         "INVALID_TYPE",
				Amount:       "100.50",
				ProviderID:   "PG_SOFT",
				ProviderTxID: "bet_12345",
			},
			wantErr: true,
			errMsg:  "Type",
		},
		{
			name: "invalid - provider_id with special chars",
			req: TransactionRequest{
				UserID:       1,
				Currency:     "TWD",
				Type:         domain.TxTypeBet,
				Amount:       "100.50",
				ProviderID:   "PG-SOFT!", // 包含特殊字元
				ProviderTxID: "bet_12345",
			},
			wantErr: true,
			errMsg:  "ProviderID",
		},
		{
			name: "invalid - missing required field",
			req: TransactionRequest{
				UserID:       1,
				Currency:     "TWD",
				Type:         domain.TxTypeBet,
				Amount:       "100.50",
				ProviderID:   "PG_SOFT",
				// ProviderTxID 缺失
			},
			wantErr: true,
			errMsg:  "ProviderTxID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.Struct(tt.req)
			if tt.wantErr {
				assert.Error(t, err)
				if err != nil {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestTransactionRequest_ToDomain(t *testing.T) {
	tests := []struct {
		name    string
		req     TransactionRequest
		wantErr bool
	}{
		{
			name: "valid conversion",
			req: TransactionRequest{
				UserID:       1,
				Currency:     "TWD",
				Type:         domain.TxTypeBet,
				Amount:       "100.50",
				ProviderID:   "PG_SOFT",
				ProviderTxID: "bet_12345",
				ReferenceID:  "round_001",
			},
			wantErr: false,
		},
		{
			name: "invalid amount format",
			req: TransactionRequest{
				UserID:       1,
				Currency:     "TWD",
				Type:         domain.TxTypeBet,
				Amount:       "not_a_number",
				ProviderID:   "PG_SOFT",
				ProviderTxID: "bet_12345",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domainReq, err := tt.req.ToDomain()
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, domainReq)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, domainReq)
				assert.Equal(t, tt.req.UserID, domainReq.UserID)
				assert.Equal(t, tt.req.Currency, domainReq.Currency)
				assert.Equal(t, tt.req.Type, domainReq.Type)
			}
		})
	}
}
