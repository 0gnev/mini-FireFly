// k6 load smoke (SPEC §14.4). 50 RPS x 60s against POST /api/v1/search.
//
// Asserts the aggregator answers within the deadline + a small overhead margin
// (p99 < SEARCH_DEADLINE_MS + 200) and returns zero 5xx — partial results are
// still HTTP 200 (SPEC §2.4), so a "failure" here means a real server fault.
//
// Run it under MIXED CHAOS for a realistic picture, e.g.:
//   make chaos-slow P=b && make chaos-flaky P=c
//   make load
//   make chaos-reset
//
// Tunables via env: BASE_URL, RPS, DURATION, SEARCH_DEADLINE_MS.
import http from 'k6/http';
import { check } from 'k6';

const BASE = __ENV.BASE_URL || 'http://localhost:8000';
const DEADLINE_MS = Number(__ENV.SEARCH_DEADLINE_MS || 2000);

// Allowed routes (fixtures/routes.json) to spread load realistically.
const ROUTES = [
  ['BEG', 'AMS'], ['BEG', 'CDG'], ['BEG', 'FRA'], ['FRA', 'JFK'],
  ['LHR', 'JFK'], ['AMS', 'BCN'], ['CDG', 'MAD'], ['IST', 'DXB'],
];

export const options = {
  scenarios: {
    smoke: {
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.RPS || 50),
      timeUnit: '1s',
      duration: __ENV.DURATION || '60s',
      preAllocatedVUs: 60,
      maxVUs: 300,
    },
  },
  thresholds: {
    // The aggregator must answer within the deadline + a small overhead margin.
    http_req_duration: [`p(99)<${DEADLINE_MS + 200}`],
    // Zero server errors (partial results are still 200, SPEC §2.4).
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
  },
};

function futureDate() {
  const days = 3 + Math.floor(Math.random() * 180);
  return new Date(Date.now() + days * 86400000).toISOString().slice(0, 10);
}

export default function () {
  const [origin, destination] = ROUTES[Math.floor(Math.random() * ROUTES.length)];
  const body = JSON.stringify({ origin, destination, depart_date: futureDate(), passengers: 1 });
  const res = http.post(`${BASE}/api/v1/search`, body, {
    headers: { 'Content-Type': 'application/json' },
  });
  check(res, {
    'status is 200': (r) => r.status === 200,
    'no 5xx': (r) => r.status < 500,
    'has providers map': (r) => {
      try { return r.json('providers') !== undefined; } catch (_e) { return false; }
    },
  });
}
