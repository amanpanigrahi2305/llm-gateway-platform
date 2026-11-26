// Steady traffic for Kubernetes tests: delete a pod, roll a deployment or watch
// the HPA while this runs, then read the result counts. Uses only the mock
// provider (no LLM cost).
//
//   k6 run -e GATEWAY_URL=http://127.0.0.1:8088 -e GATEWAY_KEY=sk-gw-... loadtest/steady.js
//
// Options (with -e): GATEWAY_URL, MODEL, VUS, DURATION, RATE.
// - Without RATE: VUS virtual users send requests back to back (a closed
//   model: fast failures make a user send more, so counts favor errors).
// - With RATE: exactly RATE requests per second whatever the answers are (an
//   open model, like real users), so the error share is the user impact.
// Each request is counted once in "outcome" with a result tag:
//   ok          status 200
//   http_<code> any other HTTP status (for example http_502)
//   conn_error  no HTTP response at all (connection refused or reset)
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const outcome = new Counter('outcome');
const rate = Number(__ENV.RATE || 0);
const duration = __ENV.DURATION || '60s';

const thresholds = {
  http_req_failed: ['rate<0.01'],
  // Listed so the summary shows each count, even when it is zero.
  'outcome{result:ok}': ['count>=0'],
  'outcome{result:conn_error}': ['count>=0'],
  'outcome{result:http_502}': ['count>=0'],
  'outcome{result:http_503}': ['count>=0'],
  'outcome{result:http_504}': ['count>=0'],
};

export const options = rate > 0
  ? {
      scenarios: {
        steady: {
          executor: 'constant-arrival-rate',
          rate,
          timeUnit: '1s',
          duration,
          preAllocatedVUs: Number(__ENV.VUS || 50),
          maxVUs: Number(__ENV.MAX_VUS || 200),
        },
      },
      thresholds,
    }
  : { vus: Number(__ENV.VUS || 20), duration, thresholds };

const url = (__ENV.GATEWAY_URL || 'http://127.0.0.1:8088') + '/v1/chat/completions';
const params = {
  headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${__ENV.GATEWAY_KEY}` },
  timeout: '10s',
};
const body = JSON.stringify({
  model: __ENV.MODEL || 'fast',
  messages: [{ role: 'user', content: 'load test' }],
  max_tokens: 5,
});

export default function () {
  const res = http.post(url, body, params);
  check(res, { 'status is 200': (r) => r.status === 200 });
  let result = 'ok';
  if (res.status === 0) result = 'conn_error';
  else if (res.status !== 200) result = `http_${res.status}`;
  outcome.add(1, { result });
}
