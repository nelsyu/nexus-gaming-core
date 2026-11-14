package validator

import (
	"regexp"
	"strings"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/go-playground/validator/v10"
	"github.com/shopspring/decimal"
)

var (
	// alphanumUnderscoreRegex 只允許字母、數字、底線
	alphanumUnderscoreRegex = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
)

// RegisterCustomValidators 註冊所有自定義驗證器到 Gin 的 validator
func RegisterCustomValidators(v *validator.Validate) error {
	// 驗證 TransactionType 是否為合法枚舉值
	if err := v.RegisterValidation("transaction_type", validateTransactionType); err != nil {
		return err
	}

	// 驗證 decimal 是否為正數
	if err := v.RegisterValidation("decimal_positive", validateDecimalPositive); err != nil {
		return err
	}

	// 驗證字串是否只包含字母、數字、底線
	if err := v.RegisterValidation("alphanum_underscore", validateAlphanumUnderscore); err != nil {
		return err
	}

	// 驗證字串是否為大寫
	if err := v.RegisterValidation("uppercase", validateUppercase); err != nil {
		return err
	}

	return nil
}

// validateTransactionType 驗證交易類型是否合法
func validateTransactionType(fl validator.FieldLevel) bool {
	txType := domain.TransactionType(fl.Field().String())

	validTypes := []domain.TransactionType{
		domain.TxTypeBet,
		domain.TxTypeWin,
		domain.TxTypeRefund,
		domain.TxTypeDeposit,
		domain.TxTypeWithdraw,
	}

	for _, valid := range validTypes {
		if txType == valid {
			return true
		}
	}

	return false
}

// validateDecimalPositive 驗證 decimal 字串是否為正數
func validateDecimalPositive(fl validator.FieldLevel) bool {
	amountStr := fl.Field().String()

	// 嘗試解析為 decimal
	amount, err := decimal.NewFromString(amountStr)
	if err != nil {
		return false
	}

	// 必須大於 0
	return amount.GreaterThan(decimal.Zero)
}

// validateAlphanumUnderscore 驗證字串只包含字母、數字、底線
func validateAlphanumUnderscore(fl validator.FieldLevel) bool {
	return alphanumUnderscoreRegex.MatchString(fl.Field().String())
}

// validateUppercase 驗證字串是否為大寫
func validateUppercase(fl validator.FieldLevel) bool {
	str := fl.Field().String()
	return str == strings.ToUpper(str)
}
