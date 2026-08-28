package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/config"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

type imageModerationProberFake struct {
	calls atomic.Int64
	err   error
}

func (p *imageModerationProberFake) ProbeImageModeration(context.Context) error {
	p.calls.Add(1)
	return p.err
}

func TestAssertImageModerationAvailableRejectsAuthorizationFailureInProduction(t *testing.T) {
	prober := &imageModerationProberFake{err: service.NewImageModerationProbeError(
		service.ImageModerationProbeAuthorization, errors.New("AccessDenied"),
	)}
	err := assertImageModerationAvailable(
		context.Background(), config.Config{Profile: config.ProfileProd}, prober,
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "凭据、权限或开通状态不可用")
	require.EqualValues(t, 1, prober.calls.Load())
}

func TestAssertImageModerationAvailableAllowsNetworkFailureInProduction(t *testing.T) {
	prober := &imageModerationProberFake{err: service.NewImageModerationProbeError(
		service.ImageModerationProbeTransient, errors.New("network timeout"),
	)}
	var output bytes.Buffer
	err := assertImageModerationAvailable(
		context.Background(), config.Config{Profile: config.ProfileProd}, prober,
		slog.New(slog.NewTextHandler(&output, nil)),
	)
	require.NoError(t, err)
	require.EqualValues(t, 1, prober.calls.Load())
	require.Contains(t, output.String(), "暂时性错误")
}

func TestAssertImageModerationAvailableSkipsDevelopment(t *testing.T) {
	prober := &imageModerationProberFake{err: errors.New("must not be called")}
	err := assertImageModerationAvailable(
		context.Background(), config.Config{Profile: config.ProfileDev}, prober,
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	)
	require.NoError(t, err)
	require.Zero(t, prober.calls.Load())
}

type emptyImageModerationRetryWorker struct{ calls atomic.Int64 }

func (w *emptyImageModerationRetryWorker) RunBatch(
	context.Context,
) (service.ImageModerationRetryWorkerResult, error) {
	w.calls.Add(1)
	return service.ImageModerationRetryWorkerResult{}, nil
}

func TestRunImageModerationRetryLoopStopsCleanlyWithoutLoggingEmptyBatches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	worker := &emptyImageModerationRetryWorker{}
	var output bytes.Buffer
	done := make(chan struct{})
	go runImageModerationRetryLoop(
		ctx, worker, time.Millisecond, slog.New(slog.NewTextHandler(&output, nil)), done,
	)
	require.Eventually(t, func() bool { return worker.calls.Load() >= 2 }, time.Second, time.Millisecond)
	cancel()
	require.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	require.Empty(t, output.String())
}
