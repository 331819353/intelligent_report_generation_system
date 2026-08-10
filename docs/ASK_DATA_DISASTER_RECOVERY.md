# AskData 灾备与图重建演练

AskData 以 PostgreSQL `askdata` schema 为语义事实源；NebulaGraph 是指定
Semantic Release 的可重建投影。恢复操作必须在隔离环境先演练，生产执行前另行完成变更审批。

## PostgreSQL 备份与恢复

备份账号需要读取 `askdata` schema 和执行 `askdata.release_manifest_hash`：

```sh
ASKDATA_CONTROL_DATABASE_URL='postgres://...' \
  ./scripts/askdata-postgres-backup.sh --output /secure/askdata-backup-20260810
```

输出包含完整控制库的 custom-format archive、逐 release 的规范 manifest、逐 AskData 表行数和
`SHA256SUMS`。控制库整体备份是为了保持 tenant/user 与语义对象、评测/报表引用的外键闭包。
恢复脚本只接受没有用户表的空数据库，绝不覆盖已有数据库，并要求显式传入确认参数：

```sh
ASKDATA_CONTROL_DATABASE_URL='postgres://isolated-restore-target/...' \
  ./scripts/askdata-postgres-restore.sh \
  --backup /secure/askdata-backup-20260810 --confirm-empty-target
```

恢复完成后脚本重新计算每个 release 的 object count 与 manifest hash；任一差异都以失败退出。

## 从指定 release 重建 NebulaGraph

该演练会删除 `ASKDATA_NEBULA_SPACE`，只能指向专门的演练 Space。脚本先从 PostgreSQL
构建只读 canonical proof，再删除/初始化 Space、以 Worker USER 投影，最后比较重建前后
release hash、graph hash、vertex/edge/object count。只有收据完全相同才通过。

```sh
set -a
. ./.env
set +a
./scripts/askdata-graph-rebuild.sh \
  --tenant-id 00000000-0000-0000-0000-000000000001 \
  --release-id 00000000-0000-0000-0000-000000000002 \
  --confirm-drop-space "$ASKDATA_NEBULA_SPACE"
```

可先执行不写图的基线检查：

```sh
go run ./cmd/askdata-graph-rebuild \
  --tenant-id "$TENANT_ID" --release-id "$RELEASE_ID"
```

演练证据应归档备份目录的 `SHA256SUMS`、恢复日志以及图重建 JSON 收据。任何 manifest
不一致、release 非 `READY/ACTIVE/SUPERSEDED/RETAINED`、父子对象不闭包或图写入失败都会
失败关闭。
