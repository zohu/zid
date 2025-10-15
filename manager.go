package zid

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	expirationDurationSec = 30 // 过期时间
	renewIntervalSec      = 10 // 心跳间隔
)

type WorkerIdManager interface {
	Acquire(ctx context.Context, max int64) error
	StartRenewal()
	Stop()
	GetWorkerId() int64
}

func WithOptionsAndWorkerManager(manager WorkerIdManager, options *Options) error {
	options = firstTruth(options, new(Options))
	if err := options.Validate(); err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := manager.Acquire(ctx, options.MaxWorkerIdNumber()); err != nil {
		return fmt.Errorf("failed to acquire workerId: %v", err)
	}
	options.WorkerId = manager.GetWorkerId()
	slog.Info("Successfully acquired workerId", "workerId", options.WorkerId)

	WithOptions(options)

	// 启动续约
	go manager.StartRenewal()
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh
		manager.Stop()
		slog.Info("zid shutdown complete")
	}()

	return nil
}
