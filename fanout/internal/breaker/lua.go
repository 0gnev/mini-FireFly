package breaker

// allowScript decides whether a call may proceed and performs time-driven
// transitions atomically.
//
// KEYS[1] = breaker:{provider}
// ARGV[1] = now (unix millis)
// ARGV[2] = cooldown_s
// ARGV[3] = half_open_max
// ARGV[4] = window_s
//
// Returns 1 (allow) or 0 (deny / breaker_open).
//
// Hash fields: state, failures, opened_at (millis), probe_inflight,
// window_start (millis).
const allowScript = `
local k = KEYS[1]
local now = tonumber(ARGV[1])
local cooldown_ms = tonumber(ARGV[2]) * 1000
local half_open_max = tonumber(ARGV[3])
local window_ms = tonumber(ARGV[4]) * 1000

local state = redis.call('HGET', k, 'state')
if not state then
  -- Fresh breaker: closed, allow.
  redis.call('HSET', k, 'state', 'closed', 'failures', 0, 'window_start', now, 'probe_inflight', 0)
  return 1
end

if state == 'closed' then
  -- Slide the failure window: if the window elapsed, reset the counter.
  local ws = tonumber(redis.call('HGET', k, 'window_start')) or now
  if now - ws >= window_ms then
    redis.call('HSET', k, 'failures', 0, 'window_start', now)
  end
  return 1
end

if state == 'open' then
  local opened_at = tonumber(redis.call('HGET', k, 'opened_at')) or now
  if now - opened_at >= cooldown_ms then
    -- Cooldown elapsed: move to half_open and reserve the first probe.
    redis.call('HSET', k, 'state', 'half_open', 'probe_inflight', 1)
    return 1
  end
  return 0
end

if state == 'half_open' then
  local inflight = tonumber(redis.call('HGET', k, 'probe_inflight')) or 0
  if inflight < half_open_max then
    redis.call('HINCRBY', k, 'probe_inflight', 1)
    return 1
  end
  return 0
end

-- Unknown state: be safe, reset to closed and allow.
redis.call('HSET', k, 'state', 'closed', 'failures', 0, 'window_start', now, 'probe_inflight', 0)
return 1
`

// recordScript records a call outcome and performs failure/success-driven
// transitions atomically.
//
// KEYS[1] = breaker:{provider}
// ARGV[1] = now (unix millis)
// ARGV[2] = outcome (0=success, 1=failure, 2=neutral)
// ARGV[3] = failure_threshold
// ARGV[4] = window_s
// ARGV[5] = cooldown_s
//
// Returns the resulting state string.
const recordScript = `
local k = KEYS[1]
local now = tonumber(ARGV[1])
local outcome = tonumber(ARGV[2])
local threshold = tonumber(ARGV[3])
local window_ms = tonumber(ARGV[4]) * 1000

local state = redis.call('HGET', k, 'state')
if not state then
  state = 'closed'
  redis.call('HSET', k, 'state', 'closed', 'failures', 0, 'window_start', now, 'probe_inflight', 0)
end

if state == 'half_open' then
  -- This call was a probe; release the reservation.
  local inflight = tonumber(redis.call('HGET', k, 'probe_inflight')) or 0
  if inflight > 0 then
    redis.call('HINCRBY', k, 'probe_inflight', -1)
  end
  if outcome == 1 then
    -- Probe failed: reopen, reset cooldown.
    redis.call('HSET', k, 'state', 'open', 'opened_at', now, 'probe_inflight', 0)
    return 'open'
  elseif outcome == 0 then
    -- Probe succeeded: close and reset counters.
    redis.call('HSET', k, 'state', 'closed', 'failures', 0, 'window_start', now, 'probe_inflight', 0)
    return 'closed'
  else
    -- Neutral (ignored error during probe): stay half_open, slot released.
    return 'half_open'
  end
end

if state == 'closed' then
  if outcome ~= 1 then
    -- success or neutral: a success resets the window counter; neutral leaves it.
    if outcome == 0 then
      redis.call('HSET', k, 'failures', 0, 'window_start', now)
    end
    return 'closed'
  end
  -- Failure: slide window then increment.
  local ws = tonumber(redis.call('HGET', k, 'window_start')) or now
  if now - ws >= window_ms then
    redis.call('HSET', k, 'failures', 0, 'window_start', now)
  end
  local failures = redis.call('HINCRBY', k, 'failures', 1)
  if failures >= threshold then
    redis.call('HSET', k, 'state', 'open', 'opened_at', now, 'probe_inflight', 0)
    return 'open'
  end
  return 'closed'
end

-- open: nothing to record (calls were short-circuited); leave as-is.
return state
`
