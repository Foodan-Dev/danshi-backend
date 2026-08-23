package testutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

// MockModerationProvider 是测试审核流水使用的稳定 provider 名称。
const MockModerationProvider model.ModerationProvider = "test_mock"

// ContentModerationRule 按目标、关键字和全局调用序号匹配文本审核请求。
// 零值字段不参与匹配；多项同时设置时必须全部命中。
type ContentModerationRule struct {
	Target   service.ModerationTarget
	Contains string
	Call     int
	Outcome  ContentModerationOutcome
}

// ContentModerationOutcome 描述一次文本审核的返回、阻塞或超时行为。
type ContentModerationOutcome struct {
	Result  service.ModerationResult
	Err     error
	Delay   time.Duration
	Release <-chan struct{}
	Timeout bool
}

// ImageModerationRule 按 object key 关键字和全局调用序号匹配图片送审请求。
type ImageModerationRule struct {
	ObjectKeyContains string
	Call              int
	Outcome           ImageModerationOutcome
}

// ImageModerationOutcome 描述一次图片送审的同步、异步或失败行为。
type ImageModerationOutcome struct {
	Submission service.ImageModerationSubmission
	Err        error
	Delay      time.Duration
	Release    <-chan struct{}
	Timeout    bool
}

// ImageCallbackOption 覆写由已提交任务生成的回调字段。
type ImageCallbackOption func(*service.ImageModerationCallback)

// ImageCallbackReceiver 是测试主动投递统一图片回调时的接收函数。
type ImageCallbackReceiver func(
	context.Context,
	service.ImageModerationCallback,
) (*service.ImageModerationApplyResult, error)

// MockModeration 同时实现文本审核和图片送审端口，并记录所有调用与回调投递。
type MockModeration struct {
	mu sync.Mutex

	contentRules   []ContentModerationRule
	contentDefault ContentModerationOutcome
	contentCalls   []service.ModerationRequest

	imageRules   []ImageModerationRule
	imageDefault ImageModerationOutcome
	imageCalls   []service.ImageModerationRequest
	jobs         map[string]imageJob

	callbackCalls []service.ImageModerationCallback
	signal        callSignal
}

type imageJob struct {
	request  service.ImageModerationRequest
	provider model.ModerationProvider
}

// NewMockModeration 创建默认同步放行文本与图片的可编程审核 Mock。
func NewMockModeration() *MockModeration {
	return &MockModeration{
		contentDefault: ContentVerdict(model.ModerationVerdictPass, nil, nil),
		imageDefault:   ImageImmediate(model.ModerationVerdictPass),
		jobs:           make(map[string]imageJob),
		signal:         newCallSignal(),
	}
}

// ContentVerdict 创建带 labels、score 的文本审核结论。
func ContentVerdict(
	verdict model.ModerationVerdict,
	labels []string,
	score *decimal.Decimal,
) ContentModerationOutcome {
	return ContentModerationOutcome{Result: service.ModerationResult{
		Provider: MockModerationProvider,
		Verdict:  verdict,
		Labels:   append([]string{}, labels...),
		Score:    cloneDecimal(score),
	}}
}

// ContentFailure 创建同步失败结论。普通 error 经 HTTP 会被归一为 500。
func ContentFailure(err error) ContentModerationOutcome {
	return ContentModerationOutcome{Err: err}
}

// ContentHTTPFailure 创建明确的 500 或 503 同步失败。
func ContentHTTPFailure(status int) ContentModerationOutcome {
	cause := fmt.Errorf("mock moderation HTTP %d", status)
	if status == http.StatusServiceUnavailable {
		return ContentFailure(apierr.ServiceUnavailable("测试审核服务不可用").WithCause(cause))
	}
	return ContentFailure(apierr.Internal(cause))
}

// ContentTimeout 一直等待调用方 context 到期并返回 context 错误。
func ContentTimeout() ContentModerationOutcome {
	return ContentModerationOutcome{Timeout: true}
}

// ContentInvalidVerdict 返回数据库和领域契约之外的 verdict。
func ContentInvalidVerdict() ContentModerationOutcome {
	return ContentVerdict(model.ModerationVerdict("invalid"), nil, nil)
}

// ImagePending 创建异步受理结论；后续用 TriggerImageCallback 主动投递。
func ImagePending(jobID string) ImageModerationOutcome {
	jobID = strings.TrimSpace(jobID)
	return ImageModerationOutcome{Submission: service.ImageModerationSubmission{
		Provider: MockModerationProvider, ProviderJobID: &jobID,
	}}
}

// ImageImmediate 创建同步图片审核结论。
func ImageImmediate(verdict model.ModerationVerdict) ImageModerationOutcome {
	return ImageModerationOutcome{Submission: service.ImageModerationSubmission{
		Provider: MockModerationProvider,
		Immediate: &service.ImageModerationCallback{
			Provider: MockModerationProvider, Verdict: verdict, Labels: []string{},
		},
	}}
}

// ImageFailure 创建图片送审同步失败结论。
func ImageFailure(err error) ImageModerationOutcome {
	return ImageModerationOutcome{Err: err}
}

// ImageTimeout 一直等待调用方 context 到期并返回 context 错误。
func ImageTimeout() ImageModerationOutcome {
	return ImageModerationOutcome{Timeout: true}
}

// SetDefaultContent 设置没有规则命中时的文本审核行为。
func (m *MockModeration) SetDefaultContent(outcome ContentModerationOutcome) {
	m.mu.Lock()
	m.contentDefault = cloneContentOutcome(outcome)
	m.mu.Unlock()
}

// ProgramContent 按传入顺序追加规则；最先命中的规则生效。
func (m *MockModeration) ProgramContent(rules ...ContentModerationRule) {
	m.mu.Lock()
	for _, rule := range rules {
		rule.Outcome = cloneContentOutcome(rule.Outcome)
		m.contentRules = append(m.contentRules, rule)
	}
	m.mu.Unlock()
}

// SetDefaultImage 设置没有规则命中时的图片送审行为。
func (m *MockModeration) SetDefaultImage(outcome ImageModerationOutcome) {
	m.mu.Lock()
	m.imageDefault = cloneImageOutcome(outcome)
	m.mu.Unlock()
}

// ProgramImage 按传入顺序追加规则；最先命中的规则生效。
func (m *MockModeration) ProgramImage(rules ...ImageModerationRule) {
	m.mu.Lock()
	for _, rule := range rules {
		rule.Outcome = cloneImageOutcome(rule.Outcome)
		m.imageRules = append(m.imageRules, rule)
	}
	m.mu.Unlock()
}

// Review 实现 service.ContentModerator。
func (m *MockModeration) Review(
	ctx context.Context,
	request service.ModerationRequest,
) (service.ModerationResult, error) {
	m.mu.Lock()
	m.contentCalls = append(m.contentCalls, cloneModerationRequest(request))
	call := len(m.contentCalls)
	outcome := cloneContentOutcome(m.contentDefault)
	for _, rule := range m.contentRules {
		if contentRuleMatches(rule, request, call) {
			outcome = cloneContentOutcome(rule.Outcome)
			break
		}
	}
	m.signal.notify()
	m.mu.Unlock()

	if err := runModerationTiming(ctx, outcome.Delay, outcome.Release, outcome.Timeout); err != nil {
		return service.ModerationResult{}, err
	}
	return cloneModerationResult(outcome.Result), outcome.Err
}

// SubmitImage 实现 service.ImageModerator。
func (m *MockModeration) SubmitImage(
	ctx context.Context,
	request service.ImageModerationRequest,
) (service.ImageModerationSubmission, error) {
	m.mu.Lock()
	m.imageCalls = append(m.imageCalls, request)
	call := len(m.imageCalls)
	outcome := cloneImageOutcome(m.imageDefault)
	for _, rule := range m.imageRules {
		if imageRuleMatches(rule, request, call) {
			outcome = cloneImageOutcome(rule.Outcome)
			break
		}
	}
	m.signal.notify()
	m.mu.Unlock()

	if err := runModerationTiming(ctx, outcome.Delay, outcome.Release, outcome.Timeout); err != nil {
		return service.ImageModerationSubmission{}, err
	}
	if outcome.Err != nil {
		return service.ImageModerationSubmission{}, outcome.Err
	}
	submission := cloneImageSubmission(outcome.Submission)
	if submission.Immediate != nil {
		submission.Immediate.ImageAssetID = request.ImageAssetID
		submission.Immediate.ObjectKey = request.ObjectKey
	}
	if submission.ProviderJobID != nil && strings.TrimSpace(*submission.ProviderJobID) != "" {
		m.mu.Lock()
		m.jobs[*submission.ProviderJobID] = imageJob{request: request, provider: submission.Provider}
		m.mu.Unlock()
	}
	return submission, nil
}

// ImageCallback 从一次已经受理的异步任务构造统一回调。
func (m *MockModeration) ImageCallback(
	jobID string,
	verdict model.ModerationVerdict,
	options ...ImageCallbackOption,
) (service.ImageModerationCallback, error) {
	m.mu.Lock()
	job, ok := m.jobs[jobID]
	m.mu.Unlock()
	if !ok {
		return service.ImageModerationCallback{}, fmt.Errorf("未知图片审核任务 %q", jobID)
	}
	callback := service.ImageModerationCallback{
		ImageAssetID:  job.request.ImageAssetID,
		ObjectKey:     job.request.ObjectKey,
		Provider:      job.provider,
		ProviderJobID: jobID,
		Verdict:       verdict,
		Labels:        []string{},
	}
	for _, option := range options {
		option(&callback)
	}
	return cloneImageCallback(callback), nil
}

// TriggerImageCallback 构造、记录并主动投递一次回调。
// 重复和乱序投递就是按测试需要重复调用、改变调用顺序。
func (m *MockModeration) TriggerImageCallback(
	ctx context.Context,
	jobID string,
	verdict model.ModerationVerdict,
	receiver ImageCallbackReceiver,
	options ...ImageCallbackOption,
) (*service.ImageModerationApplyResult, error) {
	if receiver == nil {
		return nil, errors.New("图片审核回调 receiver 不能为空")
	}
	callback, err := m.ImageCallback(jobID, verdict, options...)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.callbackCalls = append(m.callbackCalls, cloneImageCallback(callback))
	m.signal.notify()
	m.mu.Unlock()
	return receiver(ctx, callback)
}

// CallbackLabels 覆写异步回调的 labels。
func CallbackLabels(labels ...string) ImageCallbackOption {
	return func(callback *service.ImageModerationCallback) {
		callback.Labels = append([]string{}, labels...)
	}
}

// CallbackScore 覆写异步回调的 score。
func CallbackScore(score decimal.Decimal) ImageCallbackOption {
	return func(callback *service.ImageModerationCallback) {
		callback.Score = cloneDecimal(&score)
	}
}

// CallbackRawResponse 覆写异步回调的原始 JSON。
func CallbackRawResponse(raw json.RawMessage) ImageCallbackOption {
	return func(callback *service.ImageModerationCallback) {
		callback.RawResponse = append(json.RawMessage(nil), raw...)
	}
}

// CallbackObjectKey 可构造对象不匹配等负向回调。
func CallbackObjectKey(objectKey string) ImageCallbackOption {
	return func(callback *service.ImageModerationCallback) { callback.ObjectKey = objectKey }
}

// CallbackImageAssetID 可构造已删除或错误目标的负向回调。
func CallbackImageAssetID(imageAssetID uint64) ImageCallbackOption {
	return func(callback *service.ImageModerationCallback) { callback.ImageAssetID = imageAssetID }
}

// ContentCalls 返回不可变快照。
func (m *MockModeration) ContentCalls() []service.ModerationRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	calls := make([]service.ModerationRequest, len(m.contentCalls))
	for index := range m.contentCalls {
		calls[index] = cloneModerationRequest(m.contentCalls[index])
	}
	return calls
}

// ImageCalls 返回不可变快照。
func (m *MockModeration) ImageCalls() []service.ImageModerationRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]service.ImageModerationRequest{}, m.imageCalls...)
}

// CallbackCalls 返回按投递顺序排列的不可变快照。
func (m *MockModeration) CallbackCalls() []service.ImageModerationCallback {
	m.mu.Lock()
	defer m.mu.Unlock()
	calls := make([]service.ImageModerationCallback, len(m.callbackCalls))
	for index := range m.callbackCalls {
		calls[index] = cloneImageCallback(m.callbackCalls[index])
	}
	return calls
}

// WaitForContentCalls 等待至少 n 次文本审核调用，不使用 sleep 轮询。
func (m *MockModeration) WaitForContentCalls(ctx context.Context, n int) bool {
	return waitForCalls(ctx, func() (int, <-chan struct{}) {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.contentCalls), m.signal.changed
	}, n)
}

// WaitForImageCalls 等待至少 n 次图片送审调用。
func (m *MockModeration) WaitForImageCalls(ctx context.Context, n int) bool {
	return waitForCalls(ctx, func() (int, <-chan struct{}) {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.imageCalls), m.signal.changed
	}, n)
}

// RequireContentCalls 断言文本审核调用次数。
func (m *MockModeration) RequireContentCalls(t testing.TB, want int) {
	t.Helper()
	if got := len(m.ContentCalls()); got != want {
		t.Fatalf("文本审核调用次数不符：want=%d got=%d", want, got)
	}
}

// RequireImageCalls 断言图片送审调用次数。
func (m *MockModeration) RequireImageCalls(t testing.TB, want int) {
	t.Helper()
	if got := len(m.ImageCalls()); got != want {
		t.Fatalf("图片审核调用次数不符：want=%d got=%d", want, got)
	}
}

// RequireCallbackOrder 断言重复和乱序回调的实际投递顺序。
func (m *MockModeration) RequireCallbackOrder(t testing.TB, jobIDs ...string) {
	t.Helper()
	calls := m.CallbackCalls()
	if len(calls) != len(jobIDs) {
		t.Fatalf("回调投递次数不符：want=%d got=%d", len(jobIDs), len(calls))
	}
	for index := range calls {
		if calls[index].ProviderJobID != jobIDs[index] {
			t.Fatalf("第 %d 次回调任务不符：want=%q got=%q", index+1,
				jobIDs[index], calls[index].ProviderJobID)
		}
	}
}

func contentRuleMatches(
	rule ContentModerationRule,
	request service.ModerationRequest,
	call int,
) bool {
	if rule.Target != "" && rule.Target != request.Target {
		return false
	}
	if rule.Contains != "" && !strings.Contains(request.Text, rule.Contains) {
		return false
	}
	return rule.Call == 0 || rule.Call == call
}

func imageRuleMatches(
	rule ImageModerationRule,
	request service.ImageModerationRequest,
	call int,
) bool {
	if rule.ObjectKeyContains != "" && !strings.Contains(request.ObjectKey, rule.ObjectKeyContains) {
		return false
	}
	return rule.Call == 0 || rule.Call == call
}

func runModerationTiming(
	ctx context.Context,
	delay time.Duration,
	release <-chan struct{},
	timeout bool,
) error {
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := waitForRelease(ctx, release); err != nil {
		return err
	}
	if timeout {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func cloneContentOutcome(outcome ContentModerationOutcome) ContentModerationOutcome {
	outcome.Result = cloneModerationResult(outcome.Result)
	return outcome
}

func cloneImageOutcome(outcome ImageModerationOutcome) ImageModerationOutcome {
	outcome.Submission = cloneImageSubmission(outcome.Submission)
	return outcome
}

func cloneModerationRequest(request service.ModerationRequest) service.ModerationRequest {
	if request.Field != nil {
		field := *request.Field
		request.Field = &field
	}
	return request
}

func cloneModerationResult(result service.ModerationResult) service.ModerationResult {
	if result.ProviderJobID != nil {
		jobID := *result.ProviderJobID
		result.ProviderJobID = &jobID
	}
	result.Labels = append([]string{}, result.Labels...)
	result.Score = cloneDecimal(result.Score)
	result.RawResponse = append(json.RawMessage(nil), result.RawResponse...)
	return result
}

func cloneImageSubmission(
	submission service.ImageModerationSubmission,
) service.ImageModerationSubmission {
	if submission.ProviderJobID != nil {
		jobID := *submission.ProviderJobID
		submission.ProviderJobID = &jobID
	}
	if submission.Immediate != nil {
		callback := cloneImageCallback(*submission.Immediate)
		submission.Immediate = &callback
	}
	return submission
}

func cloneImageCallback(callback service.ImageModerationCallback) service.ImageModerationCallback {
	callback.Labels = append([]string{}, callback.Labels...)
	callback.Score = cloneDecimal(callback.Score)
	callback.RawResponse = append(json.RawMessage(nil), callback.RawResponse...)
	return callback
}

func cloneDecimal(value *decimal.Decimal) *decimal.Decimal {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

var (
	_ service.ContentModerator = (*MockModeration)(nil)
	_ service.ImageModerator   = (*MockModeration)(nil)
)
