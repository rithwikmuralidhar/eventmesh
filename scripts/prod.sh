#!/usr/bin/env bash
# Runs the production stack on a Linux machine. See docs/self-hosting.md.
#
#   ./scripts/prod.sh init https://yourpc.tailnet.ts.net   once: create .env.prod with random secrets
#   ./scripts/prod.sh up                                   build and start (or apply changes)
#   ./scripts/prod.sh status                               containers and health
#   ./scripts/prod.sh logs [service]                       follow logs (api, worker, postgres, redis)
#   ./scripts/prod.sh down                                 stop (data is kept)
#   ./scripts/prod.sh backup                               database dump into backups/ (keeps 14 days)
#   ./scripts/prod.sh restore backups/<file>.sql.gz        replace the database with a dump
set -euo pipefail
cd "$(dirname "$0")/.."

ENV_FILE=.env.prod
compose() { docker compose --env-file "$ENV_FILE" -f docker-compose.app.yml -f docker-compose.prod.yml "$@"; }
need_env() { [ -f "$ENV_FILE" ] || { echo "No $ENV_FILE yet. Run: ./scripts/prod.sh init https://<your public address>" >&2; exit 1; }; }
random_hex() { head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'; }

case "${1:-}" in
  init)
    url="${2:-}"
    if [[ ! "$url" =~ ^https://[^/]+$ ]]; then
      echo "Usage: ./scripts/prod.sh init https://<public host>   (https, no trailing slash or path)" >&2
      exit 1
    fi
    if [ -f "$ENV_FILE" ]; then
      echo "$ENV_FILE already exists; not overwriting it (the database password and encryption key are in it)." >&2
      exit 1
    fi
    umask 077 # only your user can read the file
    cat > "$ENV_FILE" <<EOF
# Production secrets for EventMesh. Generated $(date -u +%Y-%m-%dT%H:%MZ). Never commit or share this file.
# Losing EM_PROD_ENCRYPTION_KEY makes every stored endpoint secret unreadable: keep a copy somewhere safe.
EM_PUBLIC_URL=$url
EM_PROD_DB_PASSWORD=$(random_hex)
EM_PROD_REDIS_PASSWORD=$(random_hex)
EM_PROD_ENCRYPTION_KEY=$(head -c 32 /dev/urandom | base64 | tr -d '\n')
# true only while you create your own account; then set false and run ./scripts/prod.sh up
EM_ALLOW_REGISTRATION=true
# Change only if this subnet clashes with your home network.
EM_PROD_SUBNET=172.31.240.0/24
EOF
    echo "Wrote $ENV_FILE (readable only by you). Next: ./scripts/prod.sh up"
    ;;
  up)
    need_env
    compose up --build -d --remove-orphans
    compose ps
    ;;
  down)
    need_env
    compose down # no -v: the database volume is kept
    ;;
  status)
    need_env
    compose ps
    curl -fsS http://127.0.0.1:8090/readyz && echo
    ;;
  logs)
    need_env
    shift
    compose logs -f --tail=100 "$@"
    ;;
  backup)
    need_env
    mkdir -p backups && chmod 700 backups
    file="backups/eventmesh-$(date +%Y%m%d-%H%M%S).sql.gz"
    compose exec -T postgres pg_dump -U eventmesh -d eventmesh --clean --if-exists | gzip > "$file"
    chmod 600 "$file"
    find backups -name 'eventmesh-*.sql.gz' -mtime +14 -delete
    echo "Saved $file"
    ;;
  restore)
    need_env
    file="${2:?Usage: ./scripts/prod.sh restore backups/<file>.sql.gz}"
    read -r -p "This REPLACES the current database with $file. Type yes to continue: " answer
    [ "$answer" = yes ] || { echo "Cancelled."; exit 1; }
    compose stop api worker
    gunzip -c "$file" | compose exec -T postgres psql -U eventmesh -d eventmesh -v ON_ERROR_STOP=1 -q
    compose start api worker
    echo "Restored from $file"
    ;;
  *)
    sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//'
    exit 1
    ;;
esac
