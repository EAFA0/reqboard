# ReqBoard 多仓 Demo 沙箱

`./setup.sh` 会从 `fixtures/` 重建两个真实 git 仓库（`order_api`、`order_service`，均含 `master` 与 `feature/order-visibility` 分支），并生成带真实 diff 的 `.reqboard/`。

```bash
./setup.sh
cd demo
reqboard status
reqboard check --all
reqboard todo list
reqboard dispatch --max-parallel 6
reqboard done rb-precheck      # 观察 4 道完结门禁拦截
reqboard done <todo-id>        # 前置 TODO 直接 Done，解锁 rb-vis / rb-query
```

然后用 VS Code 打开 `demo/`，执行 **ReqBoard: Open Board**。

## 预置场景

| 场景 | 位置 |
| :--- | :--- |
| 自然拓扑多层级 DAG | `.reqboard/dag.json`（`reqboard layout` 重新计算） |
| 一等前置 TODO（1→2 阻塞） | `DDL: orders add confirmation_mode` → `rb-vis` / `rb-query` |
| `out_of_scope` 红线 | `rb-refund`（退订/退款） |
| `proposed` 待批节点 | `rb-invoice`（发票抬头校验） |
| 橙色虚线 `mocked` 边 + 备注 | `rb-auth -> rb-precheck`：“因 AuthService 接口方案未确认，这条链路先 mock” |
| 已实现且精确 AST 绑定 | `rb-vis / rb-create / rb-precheck` |
| 故意 `Orphan Diff` | `order_api/pkg/ratelimit/order_limiter.go :: NewOrderRateLimiter` |
| `deprecated` 节点 `To-Delete` 残骸 | `order_api/service/visibility.go :: LegacyVisibilityScope` |
| 未覆盖 `Unmapped PRD` | PRD `## 6 发票邮寄` |
| 生成代码忽略 | `order_api/handler/order_query_mock.go`（`**/*_mock.go`） |

`done rb-precheck` 会同时命中：上游 `rb-auth`（external）未 done、`mocked` 入边、`stale` 入边。
