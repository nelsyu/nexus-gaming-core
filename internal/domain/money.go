package domain

import "github.com/shopspring/decimal"

// MoneyScale 金額精度（小數位數），必須與 DB 欄位 NUMERIC(20, 4) 對齊。
const MoneyScale = 4

// ToMinorUnits 將 decimal 金額轉為整數最小單位（例如 10.5 → 105000）。
//
// Redis 端一律用整數 (HINCRBY) 存餘額，避免浮點數累加誤差。
// 若金額小數位數超過 MoneyScale，或超出 int64 範圍，回傳 ErrInvalidAmount，
// 因為那代表 Redis 與 DB（會四捨五入到 4 位）算出來的結果會不一致。
func ToMinorUnits(d decimal.Decimal) (int64, error) {
	scaled := d.Shift(MoneyScale)
	if !scaled.IsInteger() || !scaled.BigInt().IsInt64() {
		return 0, ErrInvalidAmount
	}
	return scaled.IntPart(), nil
}
