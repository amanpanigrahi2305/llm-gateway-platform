// Failover under load. Sends steady traffic to one alias while you turn the
// primary mock's outage switch on and off, and counts failed requests and
// which provider served each request. Uses only the mock provider (no LLM cost).
//
//   k6 run -e GATEWAY_KEY=sk-gw-... loadtest/failover.js
//   (in another shell) curl -X PATCH -d '{"outage":true}'  localhost:9090/admin/settings
//                      curl -X PATCH -d '{"outage":false}' localhost:9090/admin/settings
//
// Options (with -e): GATEWAY_URL, MODEL, VUS, DURATION.
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const served = new Counter('served');

export const options = {
  vus: Number(__ENV.VUS || 20),
  duration: __ENV.DURATION || '20s',
  thresholds: {
    http_req_failed: ['rate<0.01'],
    // Listed so the summary shows the count per provider.
    'served{provider:mock-a}': ['count>=0'],
    'served{provider:mock-b}': ['count>=0'],
  },
};

const url = (__ENV.GATEWAY_URL || 'http://localhost:8080') + '/v1/chat/completions';
const params = {
  headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${__ENV.GATEWAY_KEY}` },
};
const body = JSON.stringify({
  model: __ENV.MODEL || 'fast',
  messages: [{ role: 'user', content: 'load test' }],
  max_tokens: 5,
});

export default function () {
  const res = http.post(url, body, params);
  check(res, { 'status is 200': (r) => r.status === 200 });
  served.add(1, { provider: res.headers['X-Gateway-Provider'] || 'none' });
}
