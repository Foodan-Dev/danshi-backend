package envelope

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
)

// 成功响应不能出现 error_code / error_id 字段。
func TestOKHasNoErrorFields(t *testing.T) {
	b, err := json.Marshal(OK("success", map[string]int{"id": 1}))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["error_code"]; ok {
		t.Fatalf("成功响应混入 error_code: %s", b)
	}
	if _, ok := m["error_id"]; ok {
		t.Fatalf("成功响应混入 error_id: %s", b)
	}
}

// 422 带 data.errors，其余错误 data 为 null——前端的窄化判断依赖这个不变量。
func TestFromErrorShapes(t *testing.T) {
	status, body := FromError(apierr.InvalidField("page", apierr.FieldOutOfRange, "page 不能小于 1"))
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("状态码 %d", status)
	}
	b, _ := json.Marshal(body)
	var v struct {
		ErrorCode string `json:"error_code"`
		Data      struct {
			Errors []struct {
				Field string `json:"field"`
				Code  string `json:"code"`
			} `json:"errors"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if v.ErrorCode != "validation_failed" {
		t.Fatalf("error_code=%q", v.ErrorCode)
	}
	if len(v.Data.Errors) != 1 || v.Data.Errors[0].Code != "out_of_range" {
		t.Fatalf("字段错误结构不对: %s", b)
	}

	_, body = FromError(apierr.NotFound(apierr.BizPostNotFound, "帖子"))
	b, _ = json.Marshal(body)
	var n map[string]any
	if err := json.Unmarshal(b, &n); err != nil {
		t.Fatal(err)
	}
	if n["data"] != nil {
		t.Fatalf("非 422 的 data 必须是 null: %s", b)
	}
	if n["error_code"] != "post_not_found" {
		t.Fatalf("error_code=%v", n["error_code"])
	}
}

// 500 必须带 error_id，且响应体不得泄露内部原因。
func TestInternalCarriesErrorIDWithoutLeaking(t *testing.T) {
	e := apierr.Internal(errors.New("pq: relation \"posts\" does not exist"))
	_, body := FromError(e)
	b, _ := json.Marshal(body)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	// 位置与 Python 侧一致：data.error_id，不在顶层。
	data, _ := m["data"].(map[string]any)
	if e.ErrorID == "" || data == nil || data["error_id"] != e.ErrorID {
		t.Fatalf("error_id 未按 data.error_id 透出: %s", b)
	}
	if m["message"] != "服务器内部错误" {
		t.Fatalf("500 文案被改动: %v", m["message"])
	}
	if s := string(b); contains(s, "posts") {
		t.Fatalf("响应体泄露了内部原因: %s", s)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
