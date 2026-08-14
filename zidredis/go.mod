module github.com/zohu/zid/zidredis

go 1.25

require (
	github.com/redis/go-redis/v9 v9.16.0
	github.com/zohu/zid v1.0.1
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
)

replace github.com/zohu/zid => ..
