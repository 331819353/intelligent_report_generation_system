-- 数据源发布改为“保存草稿 -> 连接测试 -> 提交审核 -> 审批后原子上线”。
CREATE TABLE platform.data_source_publication_requests(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  data_source_id uuid NOT NULL,
  data_source_version_id uuid NOT NULL,
  config_hash text NOT NULL CHECK(config_hash ~ '^[0-9a-f]{64}$'),
  status text NOT NULL DEFAULT 'PENDING'
    CHECK(status IN ('PENDING','APPROVED','REJECTED','WITHDRAWN')),
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  requester_user_id uuid NOT NULL,
  request_note text NOT NULL DEFAULT '' CHECK(length(request_note)<=1000),
  reviewer_user_id uuid,
  review_note text NOT NULL DEFAULT '' CHECK(length(review_note)<=1000),
  published_version_id uuid,
  submitted_at timestamptz NOT NULL DEFAULT now(),
  reviewed_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT data_source_publication_requests_source_fk
    FOREIGN KEY(data_source_id,tenant_id)
    REFERENCES platform.data_sources(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT data_source_publication_requests_version_fk
    FOREIGN KEY(data_source_version_id,data_source_id,tenant_id)
    REFERENCES platform.data_source_versions(id,data_source_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT data_source_publication_requests_requester_fk
    FOREIGN KEY(requester_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT data_source_publication_requests_reviewer_fk
    FOREIGN KEY(reviewer_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT data_source_publication_requests_published_fk
    FOREIGN KEY(published_version_id,data_source_id,tenant_id)
    REFERENCES platform.data_source_versions(id,data_source_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT data_source_publication_requests_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT data_source_publication_requests_decision_shape CHECK(
    (status='PENDING'
      AND reviewer_user_id IS NULL AND reviewed_at IS NULL
      AND published_version_id IS NULL AND review_note='')
    OR (status='APPROVED'
      AND reviewer_user_id IS NOT NULL AND reviewed_at IS NOT NULL
      AND published_version_id=data_source_version_id)
    OR (status='REJECTED'
      AND reviewer_user_id IS NOT NULL AND reviewed_at IS NOT NULL
      AND published_version_id IS NULL AND btrim(review_note)<>'')
    OR (status='WITHDRAWN'
      AND reviewer_user_id=requester_user_id AND reviewed_at IS NOT NULL
      AND published_version_id IS NULL)
  )
);

CREATE UNIQUE INDEX data_source_publication_requests_one_pending_idx
  ON platform.data_source_publication_requests(tenant_id,data_source_id)
  WHERE status='PENDING';
CREATE INDEX data_source_publication_requests_queue_idx
  ON platform.data_source_publication_requests(tenant_id,status,submitted_at,id);
CREATE INDEX data_source_publication_requests_source_idx
  ON platform.data_source_publication_requests(tenant_id,data_source_id,submitted_at DESC,id DESC);

CREATE OR REPLACE FUNCTION platform.enforce_data_source_publication_request_review()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION '数据源发布审核申请不可删除' USING ERRCODE='23514';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.data_source_id IS DISTINCT FROM OLD.data_source_id
    OR NEW.data_source_version_id IS DISTINCT FROM OLD.data_source_version_id
    OR NEW.config_hash IS DISTINCT FROM OLD.config_hash
    OR NEW.requester_user_id IS DISTINCT FROM OLD.requester_user_id
    OR NEW.request_note IS DISTINCT FROM OLD.request_note
    OR NEW.submitted_at IS DISTINCT FROM OLD.submitted_at THEN
    RAISE EXCEPTION '数据源发布审核申请事实不可修改' USING ERRCODE='23514';
  END IF;
  IF OLD.status<>'PENDING'
    OR NEW.status NOT IN ('APPROVED','REJECTED','WITHDRAWN')
    OR NEW.version<>OLD.version+1
    OR NEW.updated_at IS NOT DISTINCT FROM OLD.updated_at THEN
    RAISE EXCEPTION '数据源发布审核状态迁移无效' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_data_source_publication_request_review() FROM PUBLIC;

CREATE TRIGGER data_source_publication_requests_enforce_review
BEFORE UPDATE OR DELETE ON platform.data_source_publication_requests
FOR EACH ROW EXECUTE FUNCTION platform.enforce_data_source_publication_request_review();

ALTER TABLE platform.data_source_publication_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.data_source_publication_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY data_source_publication_requests_tenant_isolation
  ON platform.data_source_publication_requests
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

INSERT INTO platform.permissions(tenant_id,code,name,resource_type,action,description)
SELECT id,'data_source.publish','审批发布数据源','DATA_SOURCE','PUBLISH',
  '审核数据源发布申请，并在通过后原子切换运行配置'
FROM platform.tenants
ON CONFLICT(tenant_id,code) DO UPDATE SET
  name=EXCLUDED.name,
  resource_type=EXCLUDED.resource_type,
  action=EXCLUDED.action,
  description=EXCLUDED.description;

INSERT INTO platform.role_permissions(tenant_id,role_id,permission_id)
SELECT role.tenant_id,role.id,permission.id
FROM platform.roles AS role
JOIN platform.permissions AS permission
  ON permission.tenant_id=role.tenant_id
  AND permission.code='data_source.publish'
WHERE role.code::text IN ('platform_admin','tenant_admin','data_admin')
ON CONFLICT DO NOTHING;

COMMENT ON TABLE platform.data_source_publication_requests IS
  '绑定精确数据源草稿和连接测试证明的人工发布审核记录';
