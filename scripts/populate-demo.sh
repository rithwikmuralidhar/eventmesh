#!/usr/bin/env bash
# Fills a running EventMesh (for example the production stack) with demo data through its public API:
# five BrewBox endpoints that behave differently, an API key, and a batch of events.
#
#   ./scripts/populate-demo.sh https://your-address.trycloudflare.com [number-of-events]
#
# It asks for your EventMesh email and password (the password is not shown or saved).
# The endpoints point at httpbin.org, a public test service, so every outcome is real:
#   Demo: Warehouse         always 200                -> delivered
#   Demo: Email service     always 200                -> delivered
#   Demo: Flaky CRM         random 200/500/503        -> some retries, most delivered in the end
#   Demo: Broken partner    always 500                -> retried twice, then dead letter
#   Demo: Old URL           always 404                -> permanent error: not retried, dead letter at once
# Running it again adds another five endpoints; delete old "Demo:" endpoints in the UI if you like.
set -euo pipefail

BASE="${1:-}"
COUNT="${2:-30}"
if [[ ! "$BASE" =~ ^https?://[^/]+$ ]]; then
  echo "Usage: ./scripts/populate-demo.sh https://your-address.trycloudflare.com [number-of-events]" >&2
  exit 1
fi
API="$BASE/api/v1"
RUN=$(date +%s)
JAR="$(mktemp)"
trap 'rm -f "$JAR"' EXIT

json_escape() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'; }

# Cookie (session) requests need these two headers: the CSRF check compares Origin with the site address.
session() {
  curl -sS -b "$JAR" -c "$JAR" -H "Origin: $BASE" -H "X-Requested-With: XMLHttpRequest" \
    -H "Content-Type: application/json" "$@"
}

read -r -p "EventMesh email: " EMAIL
read -r -s -p "Password: " PASSWORD
echo

code=$(session -o /dev/null -w '%{http_code}' -X POST "$API/auth/login" \
  -d "{\"email\":\"$(json_escape "$EMAIL")\",\"password\":\"$(json_escape "$PASSWORD")\"}")
if [ "$code" != "200" ]; then
  echo "Sign-in failed (HTTP $code). Check the email, password and address." >&2
  exit 1
fi
echo "Signed in."

KEY=$(session -X POST "$API/api-keys" -d "{\"name\":\"demo-populate $(date +%Y-%m-%d_%H:%M)\"}" |
  grep -o '"key":"em_[^"]*"' | cut -d'"' -f4 || true)
if [ -z "$KEY" ]; then
  echo "Could not create an API key." >&2
  exit 1
fi
echo "Created an API key for this run (revoke it later under Account -> API keys)."

endpoint() { # name url event-types-json max-retries initial-delay-ms max-delay-ms
  local res
  res=$(session -w '\n%{http_code}' -X POST "$API/endpoints" -d "{\"name\":\"$1\",\"url\":\"$2\",\"event_types\":$3,\"max_retries\":$4,\"initial_retry_delay_ms\":$5,\"max_retry_delay_ms\":$6}")
  if [ "$(tail -n1 <<<"$res")" = "201" ]; then
    echo "  + $1"
  else
    echo "  ! $1 was not created: $(head -n1 <<<"$res")" >&2
  fi
}

echo "Creating endpoints:"
endpoint "Demo: Warehouse"      "https://httpbin.org/status/200"         '["order.paid","order.shipped"]'      3 1000 10000
endpoint "Demo: Email service"  "https://httpbin.org/status/200"         '["customer.created","order.paid"]'   3 1000 10000
endpoint "Demo: Flaky CRM"      "https://httpbin.org/status/200,500,503" '["*"]'                              4 1000 8000
endpoint "Demo: Broken partner" "https://httpbin.org/status/500"         '["order.paid","payment.failed"]'     2 1000 4000
endpoint "Demo: Old URL"        "https://httpbin.org/status/404"         '["order.shipped"]'                   3 1000 4000

TYPES=(order.paid order.paid order.shipped customer.created payment.failed)
NAMES=(Asha Ben Chen Divya Emma Farah Gopal Hana Ivan Jaya)
ok=0
fail=0
echo "Sending $COUNT events:"
for i in $(seq 1 "$COUNT"); do
  type=${TYPES[$((RANDOM % ${#TYPES[@]}))]}
  name=${NAMES[$((RANDOM % ${#NAMES[@]}))]}
  order="BB-$((1000 + i))"
  amount=$((99 + RANDOM % 900))
  case "$type" in
    customer.created) payload="{\"customer\":\"$name\",\"email\":\"${name,,}@example.com\"}" ;;
    payment.failed)   payload="{\"order_id\":\"$order\",\"amount\":$amount,\"reason\":\"card_declined\"}" ;;
    *)                payload="{\"order_id\":\"$order\",\"amount\":$amount,\"customer\":\"$name\"}" ;;
  esac
  code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$API/events" \
    -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
    -H "Idempotency-Key: demo-$RUN-$order-$type" \
    -d "{\"event_type\":\"$type\",\"payload\":$payload}")
  if [ "$code" = "202" ] || [ "$code" = "200" ]; then ok=$((ok + 1)); else fail=$((fail + 1)); echo "  event $i: HTTP $code" >&2; fi
done
echo "Sent $ok events ($fail failed)."

# The same order sent twice with the same Idempotency-Key is stored once (answer 200, not 202).
dup=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$API/events" \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -H "Idempotency-Key: demo-$RUN-dup-check" -d '{"event_type":"order.paid","payload":{"order_id":"BB-1001-dup"}}')
again=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$API/events" \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -H "Idempotency-Key: demo-$RUN-dup-check" -d '{"event_type":"order.paid","payload":{"order_id":"BB-1001-dup"}}')
echo "Duplicate check: first send HTTP $dup, same send again HTTP $again (expected 202 then 200)."

echo
echo "Done. Deliveries keep going for about a minute (retries). Open $BASE and press F5 now and then."
