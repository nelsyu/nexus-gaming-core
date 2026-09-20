package http

import (
	"errors"
	"net/http"
	"strings"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/gin-gonic/gin"
)

type AuthHandler struct {
	authUsecase domain.AuthUsecase
}

func NewAuthHandler(authUsecase domain.AuthUsecase) *AuthHandler {
	return &AuthHandler{
		authUsecase: authUsecase,
	}
}

type RefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// Register 處理使用者註冊
func (h *AuthHandler) Register(c *gin.Context) {
	var req domain.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "details": err.Error()})
		return
	}

	user, err := h.authUsecase.Register(c.Request.Context(), &req)
	if err != nil {
		if errors.Is(err, domain.ErrUserAlreadyExists) {
			c.JSON(http.StatusConflict, gin.H{"error": "user already exists in this tenant"})
			return
		}
		c.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"data": gin.H{
			"id":        user.ID,
			"tenant_id": user.TenantID,
			"username":  user.Username,
			"role":      user.Role,
			"currency":  user.Currency,
		},
	})
}

// Login 處理使用者登入驗證
func (h *AuthHandler) Login(c *gin.Context) {
	var req domain.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "details": err.Error()})
		return
	}

	tokenPair, err := h.authUsecase.Login(c.Request.Context(), &req)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCredentials) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid username or password"})
			return
		}
		c.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data": tokenPair,
	})
}

// RefreshToken 處理 Token 續期
func (h *AuthHandler) RefreshToken(c *gin.Context) {
	var req RefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "details": err.Error()})
		return
	}

	tokenPair, err := h.authUsecase.RefreshToken(c.Request.Context(), req.RefreshToken)
	if err != nil {
		if errors.Is(err, domain.ErrTokenExpired) || errors.Is(err, domain.ErrInvalidToken) || errors.Is(err, domain.ErrTokenRevoked) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
			return
		}
		c.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data": tokenPair,
	})
}

// Logout 處理登出：撤銷 Refresh Token 並吊銷當前 Access Token
// Header: Authorization: Bearer <access_token>（已由 AuthMiddleware 驗證）
// Body:   {"refresh_token": "..."}
func (h *AuthHandler) Logout(c *gin.Context) {
	var req domain.LogoutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "details": err.Error()})
		return
	}

	authHeader := c.GetHeader("Authorization")
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing or invalid authorization header"})
		return
	}

	if err := h.authUsecase.Logout(c.Request.Context(), parts[1], req.RefreshToken); err != nil {
		if errors.Is(err, domain.ErrForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "refresh token does not belong to current user"})
			return
		}
		c.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to logout"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "logged out successfully",
	})
}
