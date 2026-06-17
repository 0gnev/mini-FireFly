package limiter

// bucketScript is a token-bucket take-one operation.
//
// KEYS[1] = rl:{provider}
// ARGV[1] = capacity (rate_limit_rps)
// ARGV[2] = refill_rate (tokens/sec = rate_limit_rps)
// ARGV[3] = now (unix millis)
//
// Hash fields: tokens (float), ts (millis of last refill).
//
// Returns 1 if a token was granted, 0 if the bucket was empty.
//
// The key is given a 10s rolling TTL (SPEC §11.2) so idle buckets self-evict.
const bucketScript = `
local k = KEYS[1]
local capacity = tonumber(ARGV[1])
local rate = tonumber(ARGV[2])
local now = tonumber(ARGV[3])

local data = redis.call('HMGET', k, 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts = tonumber(data[2])

if tokens == nil or ts == nil then
  -- Fresh bucket starts full.
  tokens = capacity
  ts = now
end

-- Refill based on elapsed time.
local elapsed = (now - ts) / 1000.0
if elapsed < 0 then elapsed = 0 end
tokens = tokens + elapsed * rate
if tokens > capacity then tokens = capacity end

local allowed = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
end

redis.call('HSET', k, 'tokens', tokens, 'ts', now)
redis.call('PEXPIRE', k, 10000)

return allowed
`
