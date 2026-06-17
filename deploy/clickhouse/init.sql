CREATE DATABASE IF NOT EXISTS firefly;

CREATE TABLE IF NOT EXISTS firefly.provider_calls (
  ts             DateTime64(3) DEFAULT now64(3),
  request_id     String,
  provider       LowCardinality(String),
  status         LowCardinality(String),
  latency_ms     UInt32,
  attempts       UInt8,
  breaker_state  LowCardinality(String),
  cache_hit      UInt8,            -- request-level flag, denormalized per row
  offers_count   UInt16,
  partial        UInt8,
  deadline_ms    UInt16,
  origin         FixedString(3),
  destination    FixedString(3)
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (provider, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY;
