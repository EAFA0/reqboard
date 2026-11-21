#!/usr/bin/env bash
# Build the ReqBoard demo sandbox from fixtures.
# Two real git repos (master + feature/order-visibility) + a populated .reqboard workspace.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$ROOT/.." && pwd)"
RB="${REQBOARD:-$REPO_ROOT/bin/reqboard}"

if [[ ! -x "$RB" ]]; then
  echo "[demo] building reqboard..."
  (cd "$REPO_ROOT/cli" && go build -o "$REPO_ROOT/bin/reqboard" .)
fi

echo "[demo] resetting sandbox..."
rm -rf "$ROOT/order_api" "$ROOT/order_service" "$ROOT/.reqboard"

init_repo() {
  local name="$1"
  local dir="$ROOT/$name"
  mkdir -p "$dir"
  cp -R "$ROOT/fixtures/$name/base/." "$dir/"
  git -C "$dir" init -q -b master
  git -C "$dir" config user.email "demo@reqboard.local"
  git -C "$dir" config user.name "ReqBoard Demo"
  git -C "$dir" add -A
  git -C "$dir" commit -q -m "chore: base $name"
  git -C "$dir" checkout -q -b feature/order-visibility
  cp -R "$ROOT/fixtures/$name/feature/." "$dir/"
  git -C "$dir" add -A
  git -C "$dir" commit -q -m "feat: order visibility changes"
}

init_repo order_api
init_repo order_service

echo "[demo] initializing .reqboard..."
"$RB" init --root "$ROOT" --name order-visibility \
  --repo order_api=order_api:master \
  --repo order_service=order_service:master >/dev/null

"$RB" prd init "$ROOT/fixtures/prd.md" --root "$ROOT" >/dev/null
cp "$ROOT/fixtures/dag.json" "$ROOT/.reqboard/dag.json"
cp "$ROOT/fixtures/bindings.json" "$ROOT/.reqboard/bindings.json"

# Record human edits so `claim` injects [人工最新调整].
"$RB" node update rb-vis --root "$ROOT" --hold false --actor human >/dev/null
"$RB" node review rb-precheck --root "$ROOT" --pass --actor human >/dev/null

# One first-class TODO blocking two downstream nodes: demonstrates 1→N gating
# and one-click Done unlocking.
"$RB" todo add "DDL: orders add confirmation_mode" --root "$ROOT" --kind ddl \
  --blocks rb-vis,rb-query --note "上线前执行；阻塞可见性/查询两条链路完结" --actor human >/dev/null

# Supplemental requirement source: review meeting notes with its own
# `meeting-review:` anchor namespace. One paragraph is attached to rb-vis, and
# the node also carries a decision excerpt ref with a demo link.
"$RB" source add "$ROOT/fixtures/meeting-review.md" --root "$ROOT" --id meeting-review \
  --title "评审补充口径" --kind meeting \
  --url "https://example.com/reqboard-demo/review" >/dev/null
"$RB" node update rb-vis --root "$ROOT" \
  --prd-ref p:3.1.1-2 --prd-ref p:3.1.1-3 --prd-ref p:3.1.1-4 \
  --prd-ref meeting-review:1-3 --actor human >/dev/null
"$RB" ref add rb-vis --root "$ROOT" \
  --title "评审拍板：线下单支付成功后可见" --kind decision \
  --url "https://example.com/reqboard-demo/decision" \
  --quote "线下签约单支付成功后立即可见，不再等待其他状态。" --actor human >/dev/null

"$RB" layout --root "$ROOT" >/dev/null

echo "[demo] sandbox ready: $ROOT"
echo "  cd $ROOT && $RB status"
echo "  $RB todo list"
echo "  open VS Code on $ROOT and run: ReqBoard: Open Board"
