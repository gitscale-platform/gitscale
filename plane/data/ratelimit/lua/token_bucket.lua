-- Token bucket rate limiter.
-- State stored as a Redis HASH with fields: tokens (float), last_ms (int).
-- Single round-trip, cluster-safe (one key).
--
-- KEYS[1] = bucket key
-- ARGV[1] = capacity (max tokens)
-- ARGV[2] = refill_per_sec
-- ARGV[3] = now_unix_ms
-- ARGV[4] = take_n (tokens to consume; may be fractional)
-- ARGV[5] = ttl_ms  (key expiry; typically 2x the refill window)
local capacity = tonumber(ARGV[1])
local refill   = tonumber(ARGV[2])
local now_ms   = tonumber(ARGV[3])
local n        = tonumber(ARGV[4])
local ttl_ms   = tonumber(ARGV[5])

local state   = redis.call('HMGET', KEYS[1], 'tokens', 'last_ms')
local tokens  = tonumber(state[1]) or capacity
local last_ms = tonumber(state[2]) or now_ms

local elapsed_s = (now_ms - last_ms) / 1000
tokens = math.min(capacity, tokens + elapsed_s * refill)

local granted = 0
if tokens >= n then
  tokens  = tokens - n
  granted = 1
end

redis.call('HMSET', KEYS[1], 'tokens', tokens, 'last_ms', now_ms)
redis.call('PEXPIRE', KEYS[1], ttl_ms)

return {granted, tostring(tokens)}
