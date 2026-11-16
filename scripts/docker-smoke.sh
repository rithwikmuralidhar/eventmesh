#!/usr/bin/env bash
# End-to-end check of the containerised stack (docker-compose.app.yml): register, create an endpoint that
# points at the receiver container, send one event, and wait for it to be delivered. Used by CI.
set -euo pipefail
API=${API:-http://127.0.0.1:8090}
jar=$(mktemp)
hdr=(-H 'Content-Type: application/json' -H 'X-Requested-With: smoke')
email="smoke-$(date +%s)@example.com"

# The stack is started detached, so wait for the API (which itself waits for the migration).
for i in $(seq 1 120); do curl -fsS "$API/readyz" >/dev/null 2>&1 && break; sleep 1; done
curl -fsS "$API/readyz"; echo
curl -fsS -o /dev/null "$API/" && echo "UI served"
curl -fsS -c "$jar" "${hdr[@]}" -X POST "$API/api/v1/auth/register" -d "{\"email\":\"$email\",\"password\":\"smoke-password-1\"}" >/dev/null
curl -fsS -b "$jar" "${hdr[@]}" -X POST "$API/api/v1/endpoints" \
  -d '{"name":"smoke","url":"http://receiver:9000/hooks/ok","event_types":["*"]}' >/dev/null
key=$(curl -fsS -b "$jar" "${hdr[@]}" -X POST "$API/api/v1/api-keys" -d '{"name":"smoke"}' | sed -n 's/.*"key":"\([^"]*\)".*/\1/p')
[ -n "$key" ] || { echo "no API key returned"; exit 1; }
code=$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $key" -H 'Content-Type: application/json' \
  -X POST "$API/api/v1/events" -d '{"event_type":"smoke.test","payload":{"n":1}}')
[ "$code" = 202 ] || { echo "ingest returned $code"; exit 1; }
for i in $(seq 1 40); do
  body=$(curl -fsS -b "$jar" "$API/api/v1/deliveries")
  if echo "$body" | grep -q '"status":"delivered"'; then echo "delivered after ${i} polls"; exit 0; fi
  sleep 0.5
done
echo "delivery did not reach 'delivered': $body"; exit 1
