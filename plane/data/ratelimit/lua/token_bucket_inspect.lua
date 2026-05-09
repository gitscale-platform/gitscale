-- Token-bucket inspect (read-only).
-- Returns the recorded {capacity, tokens, refill} for KEYS[1] without
-- mutating state. Returns {0, "0", "0"} when the key is absent — callers
-- treat that as "no recorded state".
--
-- KEYS[1] = bucket key
local state = redis.call('HMGET', KEYS[1], 'tokens', 'capacity', 'refill')
local tokens   = state[1]
local capacity = state[2]
local refill   = state[3]
if tokens == false then
  return {"0", "0", "0"}
end
if capacity == false then capacity = "0" end
if refill   == false then refill   = "0" end
return {tostring(capacity), tostring(tokens), tostring(refill)}
