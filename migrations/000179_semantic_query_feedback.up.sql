BEGIN;

CREATE TABLE platform.semantic_query_feedback(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE CASCADE,
  query_plan_id uuid NOT NULL,
  actor_id uuid NOT NULL,
  rating text NOT NULL CHECK(rating IN ('ACCURATE','INACCURATE')),
  comment text NOT NULL DEFAULT '' CHECK(
    length(comment)<=2000 AND comment !~ '[[:cntrl:]]'
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_query_feedback_plan_fk
    FOREIGN KEY(query_plan_id,tenant_id)
    REFERENCES platform.semantic_query_plans(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT semantic_query_feedback_actor_fk
    FOREIGN KEY(actor_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT semantic_query_feedback_actor_plan_key
    UNIQUE(tenant_id,query_plan_id,actor_id)
);

CREATE INDEX semantic_query_feedback_quality_idx
  ON platform.semantic_query_feedback(tenant_id,rating,updated_at DESC);

ALTER TABLE platform.semantic_query_feedback ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_query_feedback FORCE ROW LEVEL SECURITY;
CREATE POLICY semantic_query_feedback_tenant_isolation
  ON platform.semantic_query_feedback
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

CREATE TRIGGER semantic_query_feedback_set_updated_at
BEFORE UPDATE ON platform.semantic_query_feedback
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

COMMENT ON TABLE platform.semantic_query_feedback IS
  '用户对已执行语义查询结果的结构化准确性反馈；同一用户对同一计划保留最新评价';

COMMIT;
