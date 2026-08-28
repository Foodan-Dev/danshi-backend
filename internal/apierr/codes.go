package apierr

// 本文件是**错误码的唯一清单**。加错误码只往这里加，不要在业务代码里散落字符串字面量。
//
// 分两类，用途完全不同：
//
//   FieldCode  字段级校验失败，出现在 422 的 data.errors[].code
//   BizCode    业务级失败，出现在响应体顶层的 error_code
//
// 两者都是**稳定的机读枚举**：前端据此做多语言与分支处理，不依赖 message 文案。
// 因此一旦发布就不能改名——要改语义请新增一个码，把旧码标记废弃。
//
// 命名约定：`<领域>_<情形>`，全小写下划线。领域取自 PRD 的模块划分。

// ---------------------------------------------------------------------------
// 字段级校验码（422 专用）
// ---------------------------------------------------------------------------

// FieldCode 标识单个字段为什么不合法。
type FieldCode string

// 字段级校验码。
const (
	FieldRequired      FieldCode = "required"       // 必填项缺失
	FieldTooLong       FieldCode = "too_long"       // 超长
	FieldTooShort      FieldCode = "too_short"      // 过短
	FieldOutOfRange    FieldCode = "out_of_range"   // 数值越界
	FieldInvalidFormat FieldCode = "invalid_format" // 格式不对（非整数、非日期等）
	FieldInvalidEnum   FieldCode = "invalid_enum"   // 不在允许的取值集合内
	FieldInvalidDomain FieldCode = "invalid_domain" // 邮箱域名不在白名单（§4.11）
	FieldConflict      FieldCode = "conflict"       // 与另一字段的取值互相矛盾
)

// ---------------------------------------------------------------------------
// 业务级错误码
// ---------------------------------------------------------------------------

// BizCode 标识一次业务失败的具体原因。
//
// **它与 HTTP 状态码是两个维度**：状态码回答「哪一类错误」，业务码回答「具体哪一种」。
// 例如同为 403，被封禁和不是作者需要完全不同的前端处理。
type BizCode string

// 业务级错误码，按领域分组。
const (
	// 通用

	BizInternal           BizCode = "internal_error"      // 500，具体原因只进日志
	BizNotFound           BizCode = "not_found"           // 泛化的资源不存在
	BizMethodNotAllowed   BizCode = "method_not_allowed"  // 路径存在但方法不对
	BizValidation         BizCode = "validation_failed"   // 422 的顶层码
	BizRateLimited        BizCode = "rate_limited"        // 触发频率限制
	BizUnauthorized       BizCode = "unauthorized"        // 未登录或登录已失效（401 一律用这一个）
	BizServiceUnavailable BizCode = "service_unavailable" // 下游服务暂时不可用，客户端应稍后重试（503）

	// 账号与会话

	BizEmailTaken          BizCode = "email_taken"            // 邮箱已被注册
	BizEmailDomainNotAllow BizCode = "email_domain_not_allow" // 域名不在白名单
	BizCredentialsInvalid  BizCode = "credentials_invalid"    // 邮箱或密码错误
	BizVerifyCodeInvalid   BizCode = "verify_code_invalid"    // 验证码错误或已失效
	BizVerifyCodeTooMany   BizCode = "verify_code_too_many"   // 验证码发送或尝试次数超限
	BizVerifyCodeBusy      BizCode = "verify_code_busy"       // 发验证码在途请求已达服务器上限；不是单邮箱配额超限
	BizAccountBanned       BizCode = "account_banned"         // 账号被封禁，前端要展示理由与解封时间
	BizAccountDeleted      BizCode = "account_deleted"        // 账号已注销
	BizSessionRevoked      BizCode = "session_revoked"        // 会话已被撤销（在别处登出/被踢）
	BizSessionNotFound     BizCode = "session_not_found"      // 指定设备会话不存在、已失效或不属于当前用户

	// 权限

	BizPermissionDenied BizCode = "permission_denied" // 角色权限不足
	BizNotOwner         BizCode = "not_owner"         // 已登录但不是这条内容的作者

	// 内容

	BizPostNotFound         BizCode = "post_not_found"
	BizPostNotPublished     BizCode = "post_not_published" // 草稿/待审/已驳回，对当前用户不可见
	BizPostDeleted          BizCode = "post_deleted"       // 已软删除
	BizCommentNotFound      BizCode = "comment_not_found"
	BizCommentDeleted       BizCode = "comment_deleted"
	BizNotificationNotFound BizCode = "notification_not_found"
	BizContentUnderAudit    BizCode = "content_under_audit"    // 先审后发，正在等审核结果
	BizContentRejected      BizCode = "content_rejected"       // 审核判定违规
	BizContentNotRestorable BizCode = "content_not_restorable" // 当前内容状态不满足恢复前置条件
	BizModerationNotPending BizCode = "moderation_not_pending" // 目标机审记录不存在或已经被人工处理

	// 词表与提议

	BizDictItemNotFound        BizCode = "dict_item_not_found"       // 口味/菜系/餐厅/窗口不存在或已停用
	BizDictItemInUse           BizCode = "dict_item_in_use"          // 被引用，只能停用不能删除
	BizWindowNotInCanteen      BizCode = "window_not_in_canteen"     // 窗口不属于所选餐厅
	BizSuggestionNotFound      BizCode = "suggestion_not_found"      // 词条提议不存在或当前用户不可见
	BizSuggestionClosed        BizCode = "suggestion_closed"         // 提议已是终态，不可再改
	BizSuggestionParentPending BizCode = "suggestion_parent_pending" // 窗口提议依赖的餐厅提议尚未批准
	BizTagLimitExceeded        BizCode = "tag_limit_exceeded"        // 单帖标签数超限
	BizTagNotFound             BizCode = "tag_not_found"             // 话题标签不存在
	BizTagNameConflict         BizCode = "tag_name_conflict"         // 重命名与既有标签大小写不敏感重名
	BizTagMergeTargetInvalid   BizCode = "tag_merge_target_invalid"  // 合并目标无效、已下架或与源相同

	// 图片

	BizImageNotFound             BizCode = "image_not_found"
	BizImageNotOwned             BizCode = "image_not_owned"             // 只能引用自己上传的图片
	BizImagePurposeWrong         BizCode = "image_purpose_wrong"         // 头像位置引用了帖子图片，或反之
	BizImageNotApproved          BizCode = "image_not_approved"          // 图片未通过审核，不得公开发布或绑定头像
	BizUploadNotFound            BizCode = "upload_not_found"            // 上传记录不存在
	BizUploadClosed              BizCode = "upload_closed"               // 上传记录已完成或已被回收
	BizUploadIncomplete          BizCode = "upload_incomplete"           // 对象尚未直传到存储
	BizUploadSizeMismatch        BizCode = "upload_size_mismatch"        // 对象大小与签发时声明不一致
	BizModerationCallbackInvalid BizCode = "moderation_callback_invalid" // 供应商回调形态或目标无效

	// 互动

	BizCannotFollowSelf BizCode = "cannot_follow_self"
	BizAlreadyExists    BizCode = "already_exists" // 重复点赞/收藏/关注，通常应幂等吞掉
	BizConflict         BizCode = "conflict"       // 泛化的状态冲突
)

// defaultBizCode 给没有显式指定业务码的错误兜一个，
// 保证响应体里 error_code 永远存在，前端不必判空。
func defaultBizCode(status int) BizCode {
	switch {
	case status == 401:
		return BizUnauthorized
	case status == 403:
		return BizPermissionDenied
	case status == 404:
		return BizNotFound
	case status == 409:
		return BizConflict
	case status == 422:
		return BizValidation
	case status == 503:
		return BizServiceUnavailable
	case status >= 500:
		return BizInternal
	default:
		// 剩下的 4xx 都是「请求本身有问题」，归到校验失败这一类。
		return BizValidation
	}
}
