#!/usr/bin/env bash
# Checks the production stack (docker-compose.app.yml + docker-compose.prod.yml) after
# `scripts/prod.sh init <url>` and `scripts/prod.sh up`. Used by CI.
set -euo pipefail
cd "$(dirname "$0")/.."
API=http://127.0.0.1:8090
ORIGIN=$(sed -n 's/^EM_PUBLIC_URL=//p' .env.prod)
hdr=(-H 'Content-Type: application/json' -H 'X-Requested-With: smoke' -H "Origin: $ORIGIN")
creds='{"email":"owner@example.com","password":"smoke-password-1"}'
fail() { echo "FAIL: $*"; exit 1; }
wait_ready() { for _ in $(seq 1 120); do curl -fsS "$API/readyz" >/dev/null 2>&1 && return; sleep 1; done; fail "API not ready"; }

wait_ready
echo "ready"
curl -fsS -o /dev/null "$API/" && echo "UI served"

headers=$(curl -fsS -D - -o /dev/null "$API/")
echo "$headers" | grep -qi '^strict-transport-security:' || fail "no HSTS header in production"
echo "$headers" | grep -qi '^x-frame-options: DENY' || fail "no X-Frame-Options"
echo "security headers present"

# Registration is open right after init, for the owner's own account.
reg=$(curl -fsS -D - -o /dev/null "${hdr[@]}" -X POST "$API/api/v1/auth/register" -d "$creds")
cookie_line=$(echo "$reg" | grep -i '^set-cookie: eventmesh_session=') || fail "no session cookie"
echo "$cookie_line" | grep -qi 'secure' || fail "session cookie is not Secure in production"
echo "$cookie_line" | grep -qi 'httponly' || fail "session cookie is not HttpOnly"
cookie=$(echo "$cookie_line" | sed 's/^[^:]*: \([^;]*\).*/\1/' | tr -d '\r')
echo "registered; cookie is Secure and HttpOnly"

# A wrong Origin is refused (the CSRF check uses EM_PUBLIC_URL).
code=$(curl -sS -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' -H 'X-Requested-With: smoke' \
  -H 'Origin: https://evil.example' -X POST "$API/api/v1/auth/login" -d "$creds")
[ "$code" = 403 ] || fail "foreign Origin got $code, want 403"
echo "foreign origin refused"

# No SSRF exemption in production: an endpoint on the private network must be refused.
code=$(curl -sS -o /dev/null -w '%{http_code}' "${hdr[@]}" -H "Cookie: $cookie" -X POST "$API/api/v1/endpoints" \
  -d '{"name":"internal","url":"http://postgres:5432/","event_types":["*"]}')
[ "$code" != 201 ] || fail "endpoint on the internal network was accepted"
echo "internal endpoint refused ($code)"

# The demo receiver must not be running.
if docker compose --env-file .env.prod -f docker-compose.app.yml -f docker-compose.prod.yml ps --services --status running | grep -qx receiver; then
  fail "demo receiver is running"
fi
docker compose --env-file .env.prod -f docker-compose.app.yml -f docker-compose.prod.yml ps --services --status running | grep -qx worker \
  || fail "worker is not running"
echo "worker running, receiver off"

# Close registration and restart: strangers are refused, the owner can still sign in.
sed -i 's/^EM_ALLOW_REGISTRATION=.*/EM_ALLOW_REGISTRATION=false/' .env.prod
bash scripts/prod.sh up >/dev/null
wait_ready
code=$(curl -sS -o /dev/null -w '%{http_code}' "${hdr[@]}" -X POST "$API/api/v1/auth/register" \
  -d '{"email":"stranger@example.com","password":"smoke-password-1"}')
[ "$code" = 403 ] || fail "registration still open after closing it (got $code)"
code=$(curl -sS -o /dev/null -w '%{http_code}' "${hdr[@]}" -X POST "$API/api/v1/auth/login" -d "$creds")
[ "$code" = 200 ] || fail "owner login got $code after closing registration"
echo "registration closed; owner can still log in"

bash scripts/prod.sh backup
ls backups/eventmesh-*.sql.gz >/dev/null || fail "no backup written"
gunzip -c backups/eventmesh-*.sql.gz | grep -q 'owner@example.com' || fail "backup does not contain the data"
echo "backup works"
echo "production smoke test passed"
