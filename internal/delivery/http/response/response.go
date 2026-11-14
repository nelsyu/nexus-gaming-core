package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

// ErrorResponse 統一的錯誤響應格式
type ErrorResponse struct {
	Error   string                 `json:"error"`
	Details map[string]interface{} `json:"details,omitempty"`
}

// SuccessResponse 統一的成功響應格式
type SuccessResponse struct {
	Data interface{} `json:"data"`
}

// BadRequest 回傳 400 錯誤
func BadRequest(c *gin.Context, message string, details ...map[string]interface{}) {
	resp := ErrorResponse{
		Error: message,
	}
	if len(details) > 0 {
		resp.Details = details[0]
	}
	c.JSON(http.StatusBadRequest, resp)
}

// ValidationError 回傳 400 驗證錯誤（將 validator 錯誤轉換為友善訊息）
func ValidationError(c *gin.Context, err error) {
	details := make(map[string]interface{})

	// 如果是 validator 錯誤，解析每個欄位的錯誤
	if validationErrs, ok := err.(validator.ValidationErrors); ok {
		for _, e := range validationErrs {
			details[e.Field()] = formatValidationError(e)
		}
	} else {
		// 其他解析錯誤（例如 JSON 格式錯誤）
		details["message"] = err.Error()
	}

	c.JSON(http.StatusBadRequest, ErrorResponse{
		Error:   "validation failed",
		Details: details,
	})
}

// formatValidationError 將 validator 錯誤轉換為人類可讀的訊息
func formatValidationError(e validator.FieldError) string {
	switch e.Tag() {
	case "required":
		return "this field is required"
	case "gt":
		return "must be greater than " + e.Param()
	case "gte":
		return "must be greater than or equal to " + e.Param()
	case "lt":
		return "must be less than " + e.Param()
	case "lte":
		return "must be less than or equal to " + e.Param()
	case "min":
		return "minimum length is " + e.Param()
	case "max":
		return "maximum length is " + e.Param()
	case "len":
		return "must be exactly " + e.Param() + " characters"
	case "transaction_type":
		return "must be one of: BET, WIN, DEPOSIT, WITHDRAW, REFUND"
	case "decimal_positive":
		return "must be a positive decimal number"
	case "alphanum_underscore":
		return "must contain only letters, numbers, and underscores"
	case "uppercase":
		return "must be uppercase"
	default:
		return "invalid value"
	}
}

// Unauthorized 回傳 401 錯誤
func Unauthorized(c *gin.Context, message string) {
	c.JSON(http.StatusUnauthorized, ErrorResponse{
		Error: message,
	})
}

// Forbidden 回傳 403 錯誤
func Forbidden(c *gin.Context, message string) {
	c.JSON(http.StatusForbidden, ErrorResponse{
		Error: message,
	})
}

// NotFound 回傳 404 錯誤
func NotFound(c *gin.Context, message string) {
	c.JSON(http.StatusNotFound, ErrorResponse{
		Error: message,
	})
}

// Conflict 回傳 409 錯誤
func Conflict(c *gin.Context, message string) {
	c.JSON(http.StatusConflict, ErrorResponse{
		Error: message,
	})
}

// PaymentRequired 回傳 402 錯誤
func PaymentRequired(c *gin.Context, message string) {
	c.JSON(http.StatusPaymentRequired, ErrorResponse{
		Error: message,
	})
}

// InternalServerError 回傳 500 錯誤
func InternalServerError(c *gin.Context, message string) {
	c.Error(gin.Error{Err: nil, Type: gin.ErrorTypePrivate})
	c.JSON(http.StatusInternalServerError, ErrorResponse{
		Error: message,
	})
}

// Success 回傳 200 成功響應
func Success(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, SuccessResponse{
		Data: data,
	})
}

// Created 回傳 201 成功響應
func Created(c *gin.Context, data interface{}) {
	c.JSON(http.StatusCreated, SuccessResponse{
		Data: data,
	})
}
