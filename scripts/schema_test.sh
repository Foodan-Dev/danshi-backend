#!/usr/bin/env bash
# schema 回归：必须跑**两条独立链路**，不能串成一条。
#   A 数据链路：干净库 → up → smoke
#   B 结构链路：干净库 → up → down → up
# 不能在跑过 smoke 的库上执行 down——smoke 会留下引用字典表的提议行，
# 而 dictionary_suggestions.parent_canteen_id 是 RESTRICT，种子回滚会正确地失败。
set -euo pipefail

CT=danshi-schema-test
IMG=postgres:18
cleanup() { docker rm -f "$CT" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

docker run -d --name "$CT" -e POSTGRES_PASSWORD=test -e POSTGRES_DB=danshi "$IMG" >/dev/null
for _ in $(seq 1 60); do
  docker exec "$CT" pg_isready -U postgres -d danshi >/dev/null 2>&1 && break
  sleep 1
done

TMP=$(mktemp -d)
for f in migrations/0*.sql; do
  b=$(basename "$f" .sql)
  awk '/^-- \+goose Down/{d=1} !d' "$f" > "$TMP/$b.up.sql"
  awk '/^-- \+goose Down/{d=1} d'  "$f" > "$TMP/$b.down.sql"
done
docker cp "$TMP/." "$CT:/tmp/" >/dev/null
docker cp migrations/testdata/schema_smoke.sql "$CT:/tmp/" >/dev/null

reset() { docker exec "$CT" psql -U postgres -d postgres -q -c "DROP DATABASE IF EXISTS danshi;" -c "CREATE DATABASE danshi;" >/dev/null; }
up()    { for f in "$TMP"/*.up.sql;   do docker exec "$CT" psql -X -v ON_ERROR_STOP=1 -U postgres -d danshi -q -f "/tmp/$(basename "$f")"; done; }
down()  { for f in $(ls -r "$TMP"/*.down.sql); do docker exec "$CT" psql -X -v ON_ERROR_STOP=1 -U postgres -d danshi -q -f "/tmp/$(basename "$f")"; done; }
tables(){ docker exec "$CT" psql -U postgres -d danshi -tAc "select count(*) from information_schema.tables where table_schema='public'"; }

echo "== A 数据链路 =="
reset; up
if ! docker exec "$CT" psql -X -v ON_ERROR_STOP=1 -U postgres -d danshi -f /tmp/schema_smoke.sql > "$TMP/smoke.out" 2>&1; then
  cat "$TMP/smoke.out" >&2
  exit 1
fi
echo "   PASS=$(grep -c 'NOTICE:  PASS' "$TMP/smoke.out")  FAIL=$(grep -c 'FAIL' "$TMP/smoke.out" || true)"

echo "== B 结构链路 =="
reset; up; echo "   up   $(tables) 表"
down;      echo "   down $(tables) 表"
up;        echo "   再up $(tables) 表"

echo "== C 断言可失败性 =="
reset; up
sed "s/= 34, '业务表共 34 张'/= 999, '业务表共 34 张'/" migrations/testdata/schema_smoke.sql > "$TMP/broken.sql"
docker cp "$TMP/broken.sql" "$CT:/tmp/" >/dev/null
if docker exec "$CT" psql -X -v ON_ERROR_STOP=1 -U postgres -d danshi -f /tmp/broken.sql >/dev/null 2>&1; then
  echo "   ✗ 故意改错的断言竟然通过了——门禁失效" >&2
  exit 1
fi
echo "   ✓ 故意改错 → 非 0 退出，门禁有效"
