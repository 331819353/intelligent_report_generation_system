BEGIN;

-- 问数运行的执行租约。
--
-- 为什么是独立表而不是给 askdata.question_runs 加列：
-- enforce_question_run_lifecycle 要求每次 UPDATE 都把 record_version 恰好加一，
-- 而 orchestrator.Transition 用 ExpectedVersion 做乐观锁。租约续期是与业务状态
-- 无关的高频写入，如果落在 question_runs 上，每次心跳都会顶掉 Loop 手里的版本号，
-- 造成虚假的版本冲突。放在旁路表里，心跳完全不触碰被审计的运行记录。
CREATE TABLE askdata.question_run_leases(
  run_id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  lease_owner text NOT NULL DEFAULT '' CHECK(
    length(lease_owner)<=128 AND lease_owner !~ '[[:cntrl:]]'
  ),
  lease_token uuid,
  lease_expires_at timestamptz,
  attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 100),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_question_run_leases_run_fk
    FOREIGN KEY(run_id,tenant_id)
    REFERENCES askdata.question_runs(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_question_run_leases_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  -- 租约要么完整持有，要么完整释放，不允许出现半持有状态。
  CONSTRAINT askdata_question_run_leases_shape_check CHECK(
    (lease_owner='' AND lease_token IS NULL AND lease_expires_at IS NULL)
    OR (lease_owner<>'' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
  )
);

CREATE INDEX askdata_question_run_leases_claimable_idx
  ON askdata.question_run_leases(tenant_id,lease_expires_at)
  WHERE lease_owner<>'';

ALTER TABLE askdata.question_run_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.question_run_leases FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_question_run_leases_domain_isolation
  ON askdata.question_run_leases
  USING(
    askdata.tenant_matches(tenant_id)
    AND askdata.domain_can_access(domain_id)
  )
  WITH CHECK(
    askdata.tenant_matches(tenant_id)
    AND askdata.domain_can_access(domain_id)
  );

CREATE TRIGGER askdata_question_run_leases_set_updated_at
BEFORE UPDATE ON askdata.question_run_leases
FOR EACH ROW EXECUTE FUNCTION askdata.set_updated_at();

-- claim_question_run 取一条待执行的问数运行并加租约。
--
-- 两类候选，语义不同，由 resume_mode 明确区分，调用方不得自己猜：
--   FRESH    —— 运行仍在 RECEIVED，从未被执行过，可以安全地完整执行。
--   ABANDONED —— 运行已越过 RECEIVED 且租约过期，说明上一个 Worker 中途死亡。
--
-- ABANDONED 绝不重跑：预算已按上次执行计入，工具调用可能已经打到数仓，
-- 重跑会重复扣预算并可能重复执行查询。按「失败关闭」原则，这类运行只被领取
-- 用于终结为 BLOCKED，由用户重新提问。
CREATE OR REPLACE FUNCTION askdata.claim_question_run(
  selected_tenant_id uuid,
  selected_worker_id text,
  selected_lease_seconds integer
)
-- OUT 参数一律加前缀：plpgsql 的 OUT 变量与同名列在函数体内不可区分，
-- 而 ON CONFLICT 的目标列不能用表别名限定，因此只能靠命名避开歧义。
RETURNS TABLE(
  claimed_run_id uuid,
  claimed_domain_id uuid,
  claimed_actor_id uuid,
  claimed_release_id uuid,
  claimed_release_content_hash text,
  claimed_current_state text,
  claimed_record_version bigint,
  claimed_lease_token uuid,
  claimed_attempt integer,
  claimed_resume_mode text
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF selected_tenant_id IS NULL
    OR length(btrim(selected_worker_id)) NOT BETWEEN 1 AND 128
    OR selected_worker_id ~ '[[:cntrl:]]'
    OR selected_lease_seconds NOT BETWEEN 30 AND 600 THEN
    RAISE EXCEPTION 'invalid question run lease parameters' USING ERRCODE='22023';
  END IF;

  RETURN QUERY
  WITH candidate AS (
    SELECT
      run.id,
      CASE WHEN run.current_state='RECEIVED' THEN 'FRESH' ELSE 'ABANDONED' END AS mode
    FROM askdata.question_runs AS run
    LEFT JOIN askdata.question_run_leases AS lease
      ON lease.run_id=run.id AND lease.tenant_id=run.tenant_id
    WHERE run.tenant_id=selected_tenant_id
      AND run.current_state NOT IN (
        'CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED',
        'OUT_OF_SCOPE','ANSWERED','BLOCKED'
      )
      AND (
        -- 从未被领取，或上一个持有者的租约已过期。
        lease.run_id IS NULL
        OR lease.lease_owner=''
        OR lease.lease_expires_at<=now()
      )
      AND COALESCE(lease.attempt,0)<3
    ORDER BY run.created_at,run.id
    FOR UPDATE OF run SKIP LOCKED
    LIMIT 1
  ), leased AS (
    INSERT INTO askdata.question_run_leases AS lease(
      run_id,tenant_id,domain_id,lease_owner,lease_token,lease_expires_at,attempt
    )
    SELECT
      run.id,run.tenant_id,run.domain_id,btrim(selected_worker_id),
      gen_random_uuid(),now()+make_interval(secs=>selected_lease_seconds),1
    FROM candidate
    JOIN askdata.question_runs AS run ON run.id=candidate.id
    ON CONFLICT(run_id) DO UPDATE SET
      lease_owner=btrim(selected_worker_id),
      lease_token=gen_random_uuid(),
      lease_expires_at=now()+make_interval(secs=>selected_lease_seconds),
      attempt=lease.attempt+1
    RETURNING lease.run_id,lease.lease_token,lease.attempt
  )
  SELECT
    run.id,run.domain_id,run.actor_id,run.release_id,run.release_content_hash,
    run.current_state,run.record_version,leased.lease_token,leased.attempt,
    candidate.mode
  FROM leased
  JOIN askdata.question_runs AS run ON run.id=leased.run_id
  JOIN candidate ON candidate.id=leased.run_id;
END
$$;

-- heartbeat_question_run 延长租约。只有持有正确 token 的 Worker 能续期，
-- 因此一个已经被判定失联、租约被别人接管的 Worker 无法把自己续回来。
CREATE OR REPLACE FUNCTION askdata.heartbeat_question_run(
  selected_run_id uuid,
  selected_lease_token uuid,
  selected_lease_seconds integer
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE extended integer := 0;
BEGIN
  IF selected_run_id IS NULL OR selected_lease_token IS NULL
    OR selected_lease_seconds NOT BETWEEN 30 AND 600 THEN
    RAISE EXCEPTION 'invalid question run heartbeat parameters' USING ERRCODE='22023';
  END IF;
  UPDATE askdata.question_run_leases
  SET lease_expires_at=now()+make_interval(secs=>selected_lease_seconds)
  WHERE run_id=selected_run_id
    AND lease_token=selected_lease_token
    AND lease_expires_at>now();
  GET DIAGNOSTICS extended = ROW_COUNT;
  RETURN extended=1;
END
$$;

-- release_question_run 主动释放租约。运行是否成功由 question_runs 的终态决定，
-- 租约表只记录执行权，不复制业务结论。
CREATE OR REPLACE FUNCTION askdata.release_question_run(
  selected_run_id uuid,
  selected_lease_token uuid
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE released integer := 0;
BEGIN
  IF selected_run_id IS NULL OR selected_lease_token IS NULL THEN
    RAISE EXCEPTION 'invalid question run release parameters' USING ERRCODE='22023';
  END IF;
  UPDATE askdata.question_run_leases
  SET lease_owner='',lease_token=NULL,lease_expires_at=NULL
  WHERE run_id=selected_run_id AND lease_token=selected_lease_token;
  GET DIAGNOSTICS released = ROW_COUNT;
  RETURN released=1;
END
$$;

REVOKE ALL ON FUNCTION
  askdata.claim_question_run(uuid,text,integer),
  askdata.heartbeat_question_run(uuid,uuid,integer),
  askdata.release_question_run(uuid,uuid)
FROM PUBLIC;

COMMENT ON TABLE askdata.question_run_leases IS
  'Execution leases for question runs; kept out of question_runs so heartbeats do not churn record_version';

COMMIT;
