-- 幂等响应必须绑定首次请求的操作者，防止同租户其他用户把键当作读取响应的旁路凭据。
ALTER TABLE platform.report_idempotency_records ADD COLUMN actor_user_id uuid;

UPDATE platform.report_idempotency_records AS request
SET actor_user_id = (
  SELECT actor_user_id
  FROM platform.report_revisions
  WHERE report_id=request.report_id AND idempotency_key=request.idempotency_key AND actor_user_id IS NOT NULL
  ORDER BY revision_no
  LIMIT 1
);

ALTER TABLE platform.report_idempotency_records
  ALTER COLUMN actor_user_id SET NOT NULL,
  ADD CONSTRAINT report_idempotency_records_actor_fk
    FOREIGN KEY(actor_user_id,tenant_id) REFERENCES platform.users(id,tenant_id);

CREATE INDEX report_idempotency_actor_idx
  ON platform.report_idempotency_records(tenant_id,actor_user_id,created_at DESC);
