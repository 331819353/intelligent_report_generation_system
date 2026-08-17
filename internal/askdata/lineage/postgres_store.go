package lineage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

const maxAdjacentBatch = 512

// PostgresStore 持久化血缘边并提供邻接读取。COMPUTED 边由 Rebuild 幂等重建
// （关闭消失的边、插入新增的边，不动 DECLARED/IMPORTED 边）。
type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

type storeEdge struct {
	Kind     EdgeKind
	FromType NodeType
	FromID   string
	FromCode string
	ToType   NodeType
	ToID     string
	ToCode   string
	Evidence string
}

// Rebuild 重建一个业务域的全部 COMPUTED 血缘边。重建在单事务内完成：
// 先由注册表与构建出处推导目标边集，再关闭消失的活跃边、插入新出现的边。
// 未变化的边保持原 valid_from，因此重复重建不产生任何写放大。
func (store *PostgresStore) Rebuild(ctx context.Context, tenantID, domainID string) (int, error) {
	if store == nil || store.pool == nil || !canonicalScope(tenantID, domainID) {
		return 0, ErrInvalidLineageRequest
	}
	total := 0
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		edges := []storeEdge{}
		for _, derive := range []func(context.Context, pgx.Tx, string) ([]storeEdge, error){
			derivePhysicalEdges, deriveSemanticModelEdges, deriveMetricEdges,
			deriveDimensionEdges, deriveHierarchyEdges, deriveKnowledgeEdges,
		} {
			derived, err := derive(ctx, tx, domainID)
			if err != nil {
				return err
			}
			edges = append(edges, derived...)
		}
		total = len(edges)
		return replaceComputedEdges(ctx, tx, tenantID, domainID, edges)
	})
	return total, err
}

// replaceComputedEdges 以 (kind,from,to) 为事实键做差量替换。
func replaceComputedEdges(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, domainID string,
	edges []storeEdge,
) error {
	if _, err := tx.Exec(ctx, `CREATE TEMPORARY TABLE lineage_rebuild(
		kind text, from_type text, from_id text, from_code text,
		to_type text, to_id text, to_code text, evidence jsonb
	) ON COMMIT DROP`); err != nil {
		return err
	}
	for _, edge := range edges {
		evidence := edge.Evidence
		if evidence == "" {
			evidence = "{}"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO lineage_rebuild(
			kind,from_type,from_id,from_code,to_type,to_id,to_code,evidence
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb)`,
			string(edge.Kind), string(edge.FromType), edge.FromID, edge.FromCode,
			string(edge.ToType), edge.ToID, edge.ToCode, evidence); err != nil {
			return err
		}
	}
	// 关闭已消失的 COMPUTED 边：历史保留，valid_to 记录失效时刻。
	if _, err := tx.Exec(ctx, `UPDATE askdata.lineage_edges AS edge
		SET valid_to=now()
		WHERE edge.tenant_id=$1 AND edge.domain_id=$2 AND edge.derivation='COMPUTED'
		  AND edge.valid_to IS NULL
		  AND NOT EXISTS(
			SELECT 1 FROM lineage_rebuild AS target
			WHERE target.kind=edge.kind AND target.from_type=edge.from_type
			  AND target.from_id=edge.from_id AND target.to_type=edge.to_type
			  AND target.to_id=edge.to_id
		  )`, tenantID, domainID); err != nil {
		return err
	}
	// 插入新增的边；既有活跃边（含 DECLARED/IMPORTED 同事实边）保持不动。
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.lineage_edges(
		tenant_id,domain_id,family,kind,from_type,from_id,from_code,
		to_type,to_id,to_code,derivation,evidence
	)
	SELECT $1,$2,
		CASE WHEN target.kind IN ('MODEL_READS_DATASET','DATASET_DERIVES_DATASET')
			THEN 'PHYSICAL' ELSE 'SEMANTIC' END,
		target.kind,target.from_type,target.from_id,target.from_code,
		target.to_type,target.to_id,target.to_code,'COMPUTED',target.evidence
	FROM lineage_rebuild AS target
	WHERE NOT EXISTS(
		SELECT 1 FROM askdata.lineage_edges AS edge
		WHERE edge.tenant_id=$1 AND edge.domain_id=$2 AND edge.valid_to IS NULL
		  AND edge.kind=target.kind AND edge.from_type=target.from_type
		  AND edge.from_id=target.from_id AND edge.to_type=target.to_type
		  AND edge.to_id=target.to_id
	)`, tenantID, domainID); err != nil {
		return err
	}
	return nil
}

// latestVersionFilter 统一“对象最新非弃用版本”的推导口径：血缘展示当前
// 语义，不为每个历史版本各画一条边。
const latestModelCTE = `latest_models AS (
	SELECT model.*,row_number() OVER(
		PARTITION BY model.model_id ORDER BY model.version_no DESC,model.id DESC
	) AS rank
	FROM askdata.semantic_models AS model
	WHERE model.domain_id=$1 AND model.status<>'DEPRECATED'
)`

func derivePhysicalEdges(ctx context.Context, tx pgx.Tx, domainID string) ([]storeEdge, error) {
	edges := []storeEdge{}
	// 模型 → 数据集版本（读依赖）。
	rows, err := tx.Query(ctx, `WITH `+latestModelCTE+`
		SELECT model.model_id::text,model.code::text,model.dataset_version_id::text
		FROM latest_models AS model WHERE model.rank=1
		ORDER BY model.code`, domainID)
	if err != nil {
		return nil, err
	}
	if err := scanEdges(rows, &edges, func(values []string) storeEdge {
		return storeEdge{
			Kind:     EdgeModelReadsDataset,
			FromType: NodeModel, FromID: values[0], FromCode: values[1],
			ToType: NodeDatasetVersion, ToID: values[2],
		}
	}, 3); err != nil {
		return nil, err
	}
	// 数据集版本 → 数据集版本（构建派生，来自最近一次成功构建的输入清单）。
	rows, err = tx.Query(ctx, `WITH latest_runs AS (
		SELECT run.id,run.dataset_version_id,row_number() OVER(
			PARTITION BY run.dataset_version_id ORDER BY run.created_at DESC,run.id DESC
		) AS rank
		FROM platform.dataset_build_runs AS run
		JOIN platform.datasets AS dataset ON dataset.id=run.dataset_id
		WHERE dataset.domain_id=$1 AND run.status='SUCCEEDED'
	)
	SELECT DISTINCT run.dataset_version_id::text,input.input_dataset_version_id::text,run.id::text
	FROM latest_runs AS run
	JOIN platform.build_run_inputs AS input ON input.build_run_id=run.id
	WHERE run.rank=1 AND input.input_dataset_version_id IS NOT NULL
	ORDER BY 1,2`, domainID)
	if err != nil {
		return nil, err
	}
	if err := scanEdges(rows, &edges, func(values []string) storeEdge {
		return storeEdge{
			Kind:     EdgeDatasetDerivesDataset,
			FromType: NodeDatasetVersion, FromID: values[0],
			ToType: NodeDatasetVersion, ToID: values[1],
			Evidence: fmt.Sprintf(`{"buildRunId":%q}`, values[2]),
		}
	}, 3); err != nil {
		return nil, err
	}
	return edges, nil
}

func deriveSemanticModelEdges(ctx context.Context, tx pgx.Tx, domainID string) ([]storeEdge, error) {
	edges := []storeEdge{}
	// 模型 join 关系（语义边；join 合同本体仍在 relationships）。
	rows, err := tx.Query(ctx, `WITH latest_relationships AS (
		SELECT relationship.*,row_number() OVER(
			PARTITION BY relationship.relationship_id
			ORDER BY relationship.version_no DESC,relationship.id DESC
		) AS rank
		FROM askdata.relationships AS relationship
		WHERE relationship.domain_id=$1 AND relationship.status<>'DEPRECATED'
	)
	SELECT left_model.model_id::text,left_model.code::text,
		right_model.model_id::text,right_model.code::text
	FROM latest_relationships AS relationship
	JOIN askdata.semantic_models AS left_model ON left_model.id=relationship.left_model_version_id
	JOIN askdata.semantic_models AS right_model ON right_model.id=relationship.right_model_version_id
	WHERE relationship.rank=1
	ORDER BY 2,4`, domainID)
	if err != nil {
		return nil, err
	}
	return edges, scanEdges(rows, &edges, func(values []string) storeEdge {
		return storeEdge{
			Kind:     EdgeModelJoinsModel,
			FromType: NodeModel, FromID: values[0], FromCode: values[1],
			ToType: NodeModel, ToID: values[2], ToCode: values[3],
		}
	}, 4)
}

func deriveMetricEdges(ctx context.Context, tx pgx.Tx, domainID string) ([]storeEdge, error) {
	edges := []storeEdge{}
	// 指标 → 模型；指标 → 度量；度量 → 逻辑字段；指标 → 指标（公式依赖）。
	rows, err := tx.Query(ctx, `WITH latest_metric_versions AS (
		SELECT version.*,row_number() OVER(
			PARTITION BY version.metric_id ORDER BY version.version_no DESC,version.id DESC
		) AS rank
		FROM askdata.metric_versions AS version
		WHERE version.domain_id=$1 AND version.status<>'DEPRECATED'
	)
	SELECT metric.id::text,metric.code::text,model.model_id::text,model.code::text
	FROM askdata.metrics AS metric
	JOIN latest_metric_versions AS version ON version.metric_id=metric.id AND version.rank=1
	JOIN askdata.semantic_models AS model ON model.id=version.semantic_model_version_id
	WHERE metric.domain_id=$1 AND metric.status<>'DEPRECATED'
	ORDER BY metric.code`, domainID)
	if err != nil {
		return nil, err
	}
	if err := scanEdges(rows, &edges, func(values []string) storeEdge {
		return storeEdge{
			Kind:     EdgeMetricUsesModel,
			FromType: NodeMetric, FromID: values[0], FromCode: values[1],
			ToType: NodeModel, ToID: values[2], ToCode: values[3],
		}
	}, 4); err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `WITH latest_metric_versions AS (
		SELECT version.*,row_number() OVER(
			PARTITION BY version.metric_id ORDER BY version.version_no DESC,version.id DESC
		) AS rank
		FROM askdata.metric_versions AS version
		WHERE version.domain_id=$1 AND version.status<>'DEPRECATED'
	)
	SELECT metric.id::text,metric.code::text,measure.measure_id::text,measure.code::text
	FROM askdata.metrics AS metric
	JOIN latest_metric_versions AS version ON version.metric_id=metric.id AND version.rank=1
	JOIN askdata.metric_version_measures AS link ON link.metric_version_id=version.id
	JOIN askdata.measures AS measure ON measure.id=link.measure_version_id
	WHERE metric.domain_id=$1 AND metric.status<>'DEPRECATED'
	ORDER BY metric.code,measure.code`, domainID)
	if err != nil {
		return nil, err
	}
	if err := scanEdges(rows, &edges, func(values []string) storeEdge {
		return storeEdge{
			Kind:     EdgeMetricUsesMeasure,
			FromType: NodeMetric, FromID: values[0], FromCode: values[1],
			ToType: NodeMeasure, ToID: values[2], ToCode: values[3],
		}
	}, 4); err != nil {
		return nil, err
	}
	// 度量 → 逻辑字段：FIELD_REF 公式的物理锚点（字段以 数据集版本:字段 表示）。
	rows, err = tx.Query(ctx, `WITH latest_measures AS (
		SELECT measure.*,row_number() OVER(
			PARTITION BY measure.measure_id ORDER BY measure.version_no DESC,measure.id DESC
		) AS rank
		FROM askdata.measures AS measure
		WHERE measure.domain_id=$1 AND measure.status<>'DEPRECATED'
	)
	SELECT measure.measure_id::text,measure.code::text,
		model.dataset_version_id::text||':'||(measure.formula_ast->>'logicalFieldId')
	FROM latest_measures AS measure
	JOIN askdata.semantic_models AS model ON model.id=measure.semantic_model_version_id
	WHERE measure.rank=1 AND measure.formula_ast->>'type'='FIELD_REF'
	  AND COALESCE(measure.formula_ast->>'logicalFieldId','')<>''
	ORDER BY measure.code`, domainID)
	if err != nil {
		return nil, err
	}
	if err := scanEdges(rows, &edges, func(values []string) storeEdge {
		return storeEdge{
			Kind:     EdgeMeasureUsesField,
			FromType: NodeMeasure, FromID: values[0], FromCode: values[1],
			ToType: NodeModelField, ToID: values[2],
		}
	}, 3); err != nil {
		return nil, err
	}
	// 指标 → 指标：公式 AST 中的 metricCode 引用。
	rows, err = tx.Query(ctx, `WITH latest_metric_versions AS (
		SELECT version.*,row_number() OVER(
			PARTITION BY version.metric_id ORDER BY version.version_no DESC,version.id DESC
		) AS rank
		FROM askdata.metric_versions AS version
		WHERE version.domain_id=$1 AND version.status<>'DEPRECATED'
	), formula_refs AS (
		SELECT metric.id AS metric_id,metric.code AS metric_code,
			ref.value AS referenced_code
		FROM askdata.metrics AS metric
		JOIN latest_metric_versions AS version ON version.metric_id=metric.id AND version.rank=1,
		LATERAL (
			SELECT DISTINCT element->>'metricCode' AS value
			FROM jsonb_path_query(version.formula_ast,'$.**') AS element
			WHERE jsonb_typeof(element)='object' AND element ? 'metricCode'
		) AS ref
		WHERE metric.domain_id=$1 AND metric.status<>'DEPRECATED' AND COALESCE(ref.value,'')<>''
	)
	SELECT source.metric_id::text,source.metric_code::text,target.id::text,target.code::text
	FROM formula_refs AS source
	JOIN askdata.metrics AS target
	  ON target.domain_id=$1 AND lower(target.code::text)=lower(source.referenced_code)
	WHERE target.id<>source.metric_id
	ORDER BY 2,4`, domainID)
	if err != nil {
		return nil, err
	}
	if err := scanEdges(rows, &edges, func(values []string) storeEdge {
		return storeEdge{
			Kind:     EdgeMetricDependsMetric,
			FromType: NodeMetric, FromID: values[0], FromCode: values[1],
			ToType: NodeMetric, ToID: values[2], ToCode: values[3],
		}
	}, 4); err != nil {
		return nil, err
	}
	// 指标 → 维度：治理的兼容声明（compatible=true）。
	rows, err = tx.Query(ctx, `WITH latest_compat AS (
		SELECT compat.*,row_number() OVER(
			PARTITION BY compat.metric_dimension_id
			ORDER BY compat.version_no DESC,compat.id DESC
		) AS rank
		FROM askdata.metric_dimension_versions AS compat
		WHERE compat.domain_id=$1 AND compat.status<>'DEPRECATED'
	)
	SELECT identity.metric_id::text,metric.code::text,
		identity.dimension_id::text,dimension.code::text
	FROM latest_compat AS compat
	JOIN askdata.metric_dimensions AS identity ON identity.id=compat.metric_dimension_id
	JOIN askdata.metrics AS metric ON metric.id=identity.metric_id
	JOIN askdata.dimensions AS dimension ON dimension.id=compat.dimension_version_id
	WHERE compat.rank=1 AND compat.compatible
	ORDER BY 2,4`, domainID)
	if err != nil {
		return nil, err
	}
	return edges, scanEdges(rows, &edges, func(values []string) storeEdge {
		return storeEdge{
			Kind:     EdgeMetricAllowsDim,
			FromType: NodeMetric, FromID: values[0], FromCode: values[1],
			ToType: NodeDimension, ToID: values[2], ToCode: values[3],
		}
	}, 4)
}

func deriveDimensionEdges(ctx context.Context, tx pgx.Tx, domainID string) ([]storeEdge, error) {
	edges := []storeEdge{}
	rows, err := tx.Query(ctx, `WITH latest_dimensions AS (
		SELECT dimension.*,row_number() OVER(
			PARTITION BY dimension.dimension_id
			ORDER BY dimension.version_no DESC,dimension.id DESC
		) AS rank
		FROM askdata.dimensions AS dimension
		WHERE dimension.domain_id=$1 AND dimension.status<>'DEPRECATED'
	)
	SELECT dimension.dimension_id::text,dimension.code::text,
		model.model_id::text,model.code::text,
		model.dataset_version_id::text||':'||dimension.logical_field_id
	FROM latest_dimensions AS dimension
	JOIN askdata.semantic_models AS model ON model.id=dimension.semantic_model_version_id
	WHERE dimension.rank=1
	ORDER BY dimension.code`, domainID)
	if err != nil {
		return nil, err
	}
	return edges, scanEdges(rows, &edges, func(values []string) storeEdge {
		return storeEdge{
			Kind:     EdgeDimensionUsesModel,
			FromType: NodeDimension, FromID: values[0], FromCode: values[1],
			ToType: NodeModel, ToID: values[2], ToCode: values[3],
			Evidence: fmt.Sprintf(`{"binding":%q}`, values[4]),
		}
	}, 5)
}

func deriveHierarchyEdges(ctx context.Context, tx pgx.Tx, domainID string) ([]storeEdge, error) {
	edges := []storeEdge{}
	rows, err := tx.Query(ctx, `WITH latest_hierarchies AS (
		SELECT hierarchy.*,row_number() OVER(
			PARTITION BY hierarchy.hierarchy_id
			ORDER BY hierarchy.version_no DESC,hierarchy.id DESC
		) AS rank
		FROM askdata.hierarchies AS hierarchy
		WHERE hierarchy.domain_id=$1 AND hierarchy.status<>'DEPRECATED'
	)
	SELECT hierarchy.hierarchy_id::text,hierarchy.code::text,
		dimension.dimension_id::text,dimension.code::text,level.ordinal::text
	FROM latest_hierarchies AS hierarchy
	JOIN askdata.hierarchy_levels AS level ON level.hierarchy_version_id=hierarchy.id
	JOIN askdata.dimensions AS dimension ON dimension.id=level.dimension_version_id
	WHERE hierarchy.rank=1
	ORDER BY hierarchy.code,level.ordinal`, domainID)
	if err != nil {
		return nil, err
	}
	return edges, scanEdges(rows, &edges, func(values []string) storeEdge {
		return storeEdge{
			Kind:     EdgeHierarchyLevel,
			FromType: NodeHierarchy, FromID: values[0], FromCode: values[1],
			ToType: NodeDimension, ToID: values[2], ToCode: values[3],
			Evidence: fmt.Sprintf(`{"ordinal":%s}`, values[4]),
		}
	}, 5)
}

func deriveKnowledgeEdges(ctx context.Context, tx pgx.Tx, domainID string) ([]storeEdge, error) {
	edges := []storeEdge{}
	rows, err := tx.Query(ctx, `WITH latest_terms AS (
		SELECT version.*,row_number() OVER(
			PARTITION BY version.business_term_id
			ORDER BY version.version_no DESC,version.id DESC
		) AS rank
		FROM askdata.business_term_versions AS version
		WHERE version.domain_id=$1 AND version.status<>'DEPRECATED'
	)
	SELECT knowledge.business_term_id::text,knowledge.code::text,
		knowledge.target_object_type,
		COALESCE(metric.id::text,dimension.dimension_id::text,''),
		COALESCE(metric.code::text,dimension.code::text,''),
		knowledge.relation
	FROM latest_terms AS knowledge
	LEFT JOIN askdata.metric_versions AS metric_version
	  ON knowledge.target_object_type='METRIC' AND metric_version.id=knowledge.target_version_id
	LEFT JOIN askdata.metrics AS metric ON metric.id=metric_version.metric_id
	LEFT JOIN askdata.dimensions AS dimension
	  ON knowledge.target_object_type='DIMENSION' AND dimension.id=knowledge.target_version_id
	WHERE knowledge.rank=1 AND knowledge.knowledge_kind<>'ALIAS'
	  AND knowledge.target_object_type IN ('METRIC','DIMENSION')
	ORDER BY knowledge.code`, domainID)
	if err != nil {
		return nil, err
	}
	return edges, scanEdges(rows, &edges, func(values []string) storeEdge {
		if values[3] == "" {
			return storeEdge{}
		}
		toType := NodeMetric
		if values[2] == "DIMENSION" {
			toType = NodeDimension
		}
		return storeEdge{
			Kind:     EdgeKnowledgeDescribes,
			FromType: NodeKnowledge, FromID: values[0], FromCode: values[1],
			ToType: toType, ToID: values[3], ToCode: values[4],
			Evidence: fmt.Sprintf(`{"relation":%q}`, values[5]),
		}
	}, 6)
}

func scanEdges(
	rows pgx.Rows,
	edges *[]storeEdge,
	build func([]string) storeEdge,
	columns int,
) error {
	defer rows.Close()
	values := make([]string, columns)
	targets := make([]any, columns)
	for index := range values {
		targets[index] = &values[index]
	}
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return err
		}
		edge := build(append([]string(nil), values...))
		if edge.Kind == "" {
			continue
		}
		*edges = append(*edges, edge)
	}
	return rows.Err()
}

// AdjacentEdges 读取一批节点的活跃相邻边，实现遍历接口。
func (store *PostgresStore) AdjacentEdges(
	ctx context.Context,
	tenantID, domainID string,
	nodes []NodeRef,
	families []Family,
	direction Direction,
) ([]Edge, error) {
	if store == nil || store.pool == nil {
		return nil, ErrLineageUnavailable
	}
	if len(nodes) == 0 {
		return []Edge{}, nil
	}
	if len(nodes) > maxAdjacentBatch {
		nodes = nodes[:maxAdjacentBatch]
	}
	keys := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if err := node.Validate(); err != nil {
			return nil, err
		}
		keys = append(keys, string(node.Type)+":"+node.ID)
	}
	familyValues := make([]string, 0, len(families))
	for _, family := range families {
		familyValues = append(familyValues, string(family))
	}
	column := "from"
	if direction == DirectionIn {
		column = "to"
	}
	query := fmt.Sprintf(`SELECT id::text,family,kind,
		from_type,from_id,from_code,to_type,to_id,to_code,derivation,valid_from
		FROM askdata.lineage_edges
		WHERE tenant_id=$1 AND domain_id=$2 AND valid_to IS NULL
		  AND family=ANY($3) AND (%s_type||':'||%s_id)=ANY($4)
		ORDER BY kind,from_code,to_code,id`, column, column)
	result := []Edge{}
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, tenantID, domainID, familyValues, keys)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var edge Edge
			var family, kind, fromType, toType, derivation string
			if err := rows.Scan(&edge.ID, &family, &kind,
				&fromType, &edge.From.ID, &edge.From.Code,
				&toType, &edge.To.ID, &edge.To.Code,
				&derivation, &edge.ValidFrom); err != nil {
				return err
			}
			edge.Family, edge.Kind = Family(family), EdgeKind(kind)
			edge.From.Type, edge.To.Type = NodeType(fromType), NodeType(toType)
			edge.Derivation = Derivation(derivation)
			result = append(result, edge)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
