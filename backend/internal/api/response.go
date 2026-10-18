// Package api 是 HTTP 接口层：Gin 路由、中间件、handler 与 SSE。
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// 业务错误码（与 docs/CONTRACT.md 1.3 一致）。
const (
	CodeOK           = 0
	CodeBadRequest   = 40001
	CodeUnauthorized = 40100
	CodeForbidden    = 40300
	CodeNotFound     = 40400
	CodeConflict     = 40900
	CodeRateLimited  = 42900
	CodeInternal     = 50000
	CodeUpstream     = 50200
	CodeNotReady     = 50300
)

// httpStatus 把业务码映射成 HTTP 状态码。
func httpStatus(code int) int {
	switch code {
	case CodeOK:
		return http.StatusOK
	case CodeBadRequest:
		return http.StatusBadRequest
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeForbidden:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeConflict:
		return http.StatusConflict
	case CodeRateLimited:
		return http.StatusTooManyRequests
	case CodeUpstream:
		return http.StatusBadGateway
	case CodeNotReady:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// Envelope 是统一响应信封。
type Envelope struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Data      any    `json:"data"`
	RequestID string `json:"request_id"`
}

// OK 返回成功响应。
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Envelope{Code: CodeOK, Message: "ok", Data: data, RequestID: RequestIDOf(c)})
}

// Fail 返回业务错误响应。
func Fail(c *gin.Context, code int, message string) {
	c.AbortWithStatusJSON(httpStatus(code), Envelope{
		Code:      code,
		Message:   message,
		Data:      nil,
		RequestID: RequestIDOf(c),
	})
}
