package apierr

import (
	"errors"
	"net/http"
	"testing"
)

// 错误码是对外契约，重复的字面量意味着两种情形在前端无法区分。
func TestBizCodesAreUnique(t *testing.T) {
	all := []BizCode{
		BizInternal, BizNotFound, BizValidation, BizRateLimited, BizUnauthorized, BizServiceUnavailable,
		BizEmailTaken, BizEmailDomainNotAllow, BizCredentialsInvalid,
		BizVerifyCodeInvalid, BizVerifyCodeTooMany, BizAccountBanned,
		BizAccountDeleted, BizSessionRevoked, BizSessionNotFound,
		BizPermissionDenied, BizNotOwner,
		BizPostNotFound, BizPostNotPublished, BizPostDeleted,
		BizCommentNotFound, BizCommentDeleted, BizContentUnderAudit, BizContentRejected,
		BizDictItemNotFound, BizDictItemInUse, BizWindowNotInCanteen,
		BizSuggestionClosed, BizTagLimitExceeded,
		BizImageNotFound, BizImageNotOwned, BizImagePurposeWrong, BizImageNotApproved,
		BizCannotFollowSelf, BizAlreadyExists, BizConflict,
	}
	seen := make(map[BizCode]bool, len(all))
	for _, c := range all {
		if c == "" {
			t.Fatal("存在空错误码")
		}
		if seen[c] {
			t.Fatalf("错误码重复: %s", c)
		}
		seen[c] = true
	}
}

// As 必须保证响应体里 error_code 永远有值，前端不需要判空。
func TestAsFillsEmptyCode(t *testing.T) {
	e := As(&Error{Status: http.StatusForbidden, Message: "x"})
	if e.Code != BizPermissionDenied {
		t.Fatalf("期望兜底为 permission_denied，实际 %q", e.Code)
	}
	if got := As(&Error{Status: http.StatusForbidden, Code: BizAccountBanned}).Code; got != BizAccountBanned {
		t.Fatalf("显式业务码被覆盖: %q", got)
	}
	if got := As(&Error{Status: http.StatusServiceUnavailable}).Code; got != BizServiceUnavailable {
		t.Fatalf("期望 503 兜底为 service_unavailable，实际 %q", got)
	}
}

// 非业务错误一律归 500，且必须带上与日志关联的 error_id。
func TestAsWrapsUnknownError(t *testing.T) {
	e := As(errors.New("boom"))
	if e.Status != http.StatusInternalServerError || e.Code != BizInternal {
		t.Fatalf("期望 500/internal_error，实际 %d/%s", e.Status, e.Code)
	}
	if e.ErrorID == "" || e.ErrorID == "unknown" {
		t.Fatalf("500 必须带 error_id，实际 %q", e.ErrorID)
	}
	if !errors.Is(e, e.Cause()) {
		t.Fatal("原始错误必须可通过 errors.Is 取回")
	}
}

// 401 一律用同一句脱敏文案，不区分令牌过期 / 会话撤销 / 格式错误。
func TestUnauthorizedIsOpaque(t *testing.T) {
	e := Unauthorized()
	if e.Message != "未登录或登录已失效" || e.Code != BizUnauthorized {
		t.Fatalf("401 文案或业务码被改动: %q / %q", e.Message, e.Code)
	}
	if e.ErrorID != "" {
		t.Fatal("401 不该带 error_id")
	}
}

func TestServiceUnavailableCarriesCause(t *testing.T) {
	cause := errors.New("SES timeout")
	e := ServiceUnavailable("验证码暂时无法发送，请稍后再试").WithCause(cause)
	if e.Status != http.StatusServiceUnavailable || e.Code != BizServiceUnavailable {
		t.Fatalf("期望 503/service_unavailable，实际 %d/%s", e.Status, e.Code)
	}
	if e.Message != "验证码暂时无法发送，请稍后再试" {
		t.Fatalf("503 文案不符: %q", e.Message)
	}
	if !errors.Is(e, cause) {
		t.Fatal("503 底层原因必须保留在错误链中")
	}
	if e.ErrorID != "" {
		t.Fatal("503 不应伪装成内部错误并生成 error_id")
	}
}

// WithCode / WithCause 必须返回副本，不能污染共享的错误值。
func TestWithersDoNotMutate(t *testing.T) {
	base := Forbidden(BizPermissionDenied, "")
	narrowed := base.WithCode(BizAccountBanned).WithCause(errors.New("banned until 2026"))
	if base.Code != BizPermissionDenied || base.Cause() != nil {
		t.Fatal("原错误被就地修改")
	}
	if narrowed.Code != BizAccountBanned || narrowed.Cause() == nil {
		t.Fatal("副本没有带上新的业务码或原因")
	}
}
