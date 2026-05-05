-- CAS (Compare-And-Swap) for CacheStore.
-- Single round-trip, cluster-safe (operates on one key).
--
-- KEYS[1] = key
-- ARGV[1] = expected bytes ("" means key must be absent)
-- ARGV[2] = replacement bytes
-- ARGV[3] = ttl_ms
local cur = redis.call('GET', KEYS[1])
if cur == false then cur = "" end
if cur ~= ARGV[1] then return 0 end
redis.call('SET', KEYS[1], ARGV[2], 'PX', tonumber(ARGV[3]))
return 1
