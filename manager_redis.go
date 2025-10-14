package zid

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisManager struct {
	redis    redis.UniversalClient
	prefix   string
	workerId int64
	ticker   *time.Ticker
}

func NewRedisManager(r redis.UniversalClient, prefix ...string) WorkerIdManager {
	if len(prefix) == 0 {
		prefix = []string{"zid"}
	}
	return &RedisManager{
		redis:  r,
		prefix: strings.Join(prefix, ":"),
	}
}

func (m *RedisManager) Acquire(ctx context.Context, max int64) error {
	for i := int64(0); i <= max; i++ {
		if m.redis.SetNX(context.Background(), m.Prefix(i), "occupied", time.Second*expirationDurationSec).Val() {
			m.workerId = i
			return nil
		}
	}
	return fmt.Errorf("all worker id [0-%d] are occupied, please extend WorkerIdBitLength", max)
}
func (m *RedisManager) StartRenewal() {
	m.ticker = time.NewTicker(time.Second * renewIntervalSec)
	for range m.ticker.C {
		m.redis.Set(context.Background(), m.Prefix(m.workerId), "occupied", time.Second*expirationDurationSec)
	}
}
func (m *RedisManager) Stop() {
	m.ticker.Stop()
}
func (m *RedisManager) GetWorkerId() int64 {
	return m.workerId
}

func (m *RedisManager) Prefix(wid int64) string {
	return fmt.Sprintf("%s:%d", strings.TrimSuffix(m.prefix, ":"), wid)
}
