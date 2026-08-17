package retrieval

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/lineage"
	"intelligent-report-generation-system/internal/askdata/search"
	"intelligent-report-generation-system/internal/embedding"
	"intelligent-report-generation-system/internal/platform/database"
)

// PostgresCatalogSearcher 是确定性巷道：对四分区治理头做精确 + 词法检索。
// 它读注册表本身而不是检索投影，因此草稿也可发现、结果永远与治理事实同步。
type PostgresCatalogSearcher struct{ pool *pgxpool.Pool }

func NewPostgresCatalogSearcher(pool *pgxpool.Pool) *PostgresCatalogSearcher {
	return &PostgresCatalogSearcher{pool: pool}
}

// 每个分区一段头版本子查询。评分合同：code 精确 1.0 > 名称精确 0.98 >
// 别名精确 0.95 > 词法（子串 0.6 + trigram 相似度，封顶 0.9）。
func catalogSearchSQL(sections []Section) string {
	parts := []string{}
	for _, section := range sections {
		switch section {
		case SectionModel:
			parts = append(parts, `SELECT 'MODEL' AS section,'MODEL' AS object_type,
				head.model_id::text AS object_id,head.id::text AS version_id,
				head.code::text AS code,head.name,head.status,head.description AS summary,
				'{}'::text[] AS aliases
			FROM (
				SELECT model.*,row_number() OVER(
					PARTITION BY model.model_id ORDER BY model.version_no DESC,model.id DESC
				) AS rank
				FROM askdata.semantic_models AS model
				WHERE model.domain_id=$1 AND model.status<>'DEPRECATED'
			) AS head WHERE head.rank=1`)
		case SectionMetric:
			parts = append(parts, `SELECT 'METRIC','METRIC',
				metric.id::text,head.id::text,metric.code::text,metric.name,head.status,
				CASE WHEN head.business_definition<>'' THEN head.business_definition
					ELSE metric.description END,
				'{}'::text[]
			FROM askdata.metrics AS metric
			JOIN (
				SELECT version.*,row_number() OVER(
					PARTITION BY version.metric_id ORDER BY version.version_no DESC,version.id DESC
				) AS rank
				FROM askdata.metric_versions AS version
				WHERE version.domain_id=$1 AND version.status<>'DEPRECATED'
			) AS head ON head.metric_id=metric.id AND head.rank=1
			WHERE metric.domain_id=$1 AND metric.status<>'DEPRECATED'`)
		case SectionDimension:
			parts = append(parts, `SELECT 'DIMENSION','DIMENSION',
				head.dimension_id::text,head.id::text,head.code::text,head.name,head.status,
				head.description,'{}'::text[]
			FROM (
				SELECT dimension.*,row_number() OVER(
					PARTITION BY dimension.dimension_id
					ORDER BY dimension.version_no DESC,dimension.id DESC
				) AS rank
				FROM askdata.dimensions AS dimension
				WHERE dimension.domain_id=$1 AND dimension.status<>'DEPRECATED'
			) AS head WHERE head.rank=1`, `SELECT 'DIMENSION','HIERARCHY',
				head.hierarchy_id::text,head.id::text,head.code::text,head.name,head.status,
				head.description,'{}'::text[]
			FROM (
				SELECT hierarchy.*,row_number() OVER(
					PARTITION BY hierarchy.hierarchy_id
					ORDER BY hierarchy.version_no DESC,hierarchy.id DESC
				) AS rank
				FROM askdata.hierarchies AS hierarchy
				WHERE hierarchy.domain_id=$1 AND hierarchy.status<>'DEPRECATED'
			) AS head WHERE head.rank=1`)
		case SectionKnowledge:
			parts = append(parts, `SELECT 'KNOWLEDGE','KNOWLEDGE',
				head.business_term_id::text,head.id::text,head.code::text,head.name,head.status,
				head.definition,head.aliases
			FROM (
				SELECT version.*,row_number() OVER(
					PARTITION BY version.business_term_id
					ORDER BY version.version_no DESC,version.id DESC
				) AS rank
				FROM askdata.business_term_versions AS version
				WHERE version.domain_id=$1 AND version.status<>'DEPRECATED'
				  AND version.knowledge_kind<>'ALIAS'
			) AS head WHERE head.rank=1`)
		}
	}
	return `WITH heads AS (
		` + strings.Join(parts, "\n\t\tUNION ALL\n\t\t") + `
	), scored AS (
		SELECT heads.*,GREATEST(
			CASE WHEN lower(heads.code)=$2 THEN 1.0
				WHEN lower(heads.name)=$2 THEN 0.98
				WHEN EXISTS(
					SELECT 1 FROM unnest(heads.aliases) AS alias(value)
					WHERE lower(alias.value)=$2
				) THEN 0.95
				ELSE 0 END,
			LEAST(0.9,
				CASE WHEN strpos(lower(heads.code||' '||heads.name||' '||COALESCE(heads.summary,'')),$2)>0
					THEN 0.6 ELSE 0 END
				+ similarity(lower(heads.code||' '||heads.name),$2))
		)::float8 AS score
		FROM heads
	)
	SELECT section,object_type,object_id,version_id,code,name,status,
		left(COALESCE(summary,''),300),score
	FROM scored WHERE score>0.05
	ORDER BY score DESC,(status='CERTIFIED') DESC,section,code
	LIMIT $3`
}

func (searcher *PostgresCatalogSearcher) Search(
	ctx context.Context,
	tenantID, domainID, query string,
	sections []Section,
	limit int,
) ([]Candidate, error) {
	if searcher == nil || searcher.pool == nil {
		return nil, ErrUnavailable
	}
	normalized := strings.ToLower(strings.TrimSpace(query))
	result := []Candidate{}
	err := database.WithTenantTx(ctx, searcher.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, catalogSearchSQL(sections), domainID, normalized, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var candidate Candidate
			var section string
			if err := rows.Scan(&section, &candidate.ObjectType, &candidate.ObjectID,
				&candidate.VersionID, &candidate.Code, &candidate.Name,
				&candidate.Status, &candidate.Summary, &candidate.Score); err != nil {
				return err
			}
			candidate.Section = Section(section)
			if candidate.Score >= 0.95 {
				candidate.Sources = []Source{SourceExact}
			} else {
				candidate.Sources = []Source{SourceLexical}
			}
			result = append(result, candidate)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// PostgresVectorSearcher 是语义相似巷道：解析 ACTIVE Release 与角色作用域，
// 嵌入查询文本，检索 Release 投影中的向量文档，并把版本命中水合为分区候选。
// 缺 Release 或嵌入服务未配置时返回降级原因而不是错误。
type PostgresVectorSearcher struct {
	pool     *pgxpool.Pool
	store    *search.PostgresRetrievalStore
	embedder embedding.Provider
}

func NewPostgresVectorSearcher(pool *pgxpool.Pool, embedder embedding.Provider) *PostgresVectorSearcher {
	return &PostgresVectorSearcher{
		pool: pool, store: search.NewPostgresRetrievalStore(pool), embedder: embedder,
	}
}

var sectionObjectTypes = map[Section][]search.ObjectType{
	SectionModel:     {search.ObjectSemanticModel},
	SectionMetric:    {search.ObjectMetric, search.ObjectMeasureLegacy},
	SectionDimension: {search.ObjectDimension},
	SectionKnowledge: {search.ObjectBusinessTerm},
}

func (searcher *PostgresVectorSearcher) Search(
	ctx context.Context,
	tenantID, domainID, actorID, query string,
	sections []Section,
	limit int,
) ([]Candidate, string, error) {
	if searcher == nil || searcher.pool == nil {
		return nil, "", ErrUnavailable
	}
	if searcher.embedder == nil || !searcher.embedder.Configured() {
		return []Candidate{}, "EMBEDDING_UNAVAILABLE", nil
	}
	scope, degradedReason, err := searcher.resolveScope(ctx, tenantID, domainID, actorID)
	if err != nil {
		return nil, "", err
	}
	if degradedReason != "" {
		return []Candidate{}, degradedReason, nil
	}
	vectors, err := searcher.embedder.Embed(ctx, []string{query})
	if err != nil || len(vectors) != 1 {
		return []Candidate{}, "EMBEDDING_FAILED", nil
	}
	objectTypes := []search.ObjectType{}
	for _, section := range sections {
		objectTypes = append(objectTypes, sectionObjectTypes[section]...)
	}
	hits, err := searcher.store.Vector(
		ctx, scope, vectors[0], searcher.embedder.Model(), objectTypes, limit,
	)
	if err != nil {
		return nil, "", err
	}
	candidates, err := searcher.hydrate(ctx, tenantID, domainID, hits)
	if err != nil {
		return nil, "", err
	}
	return candidates, "", nil
}

// resolveScope 复用问数的 Release 解析函数与角色目录构建策略作用域。
func (searcher *PostgresVectorSearcher) resolveScope(
	ctx context.Context,
	tenantID, domainID, actorID string,
) (askdata.PolicyScope, string, error) {
	var release askdata.ReleaseRef
	roleIDs := []askdata.ID{}
	noActive := false
	err := database.WithTenantTx(ctx, searcher.pool, tenantID, func(tx pgx.Tx) error {
		var releaseID, releaseHash string
		err := tx.QueryRow(ctx, `SELECT release_id::text,content_hash
			FROM askdata.resolve_question_release($1,$2,$3)`,
			tenantID, domainID, actorID).Scan(&releaseID, &releaseHash)
		if errors.Is(err, pgx.ErrNoRows) {
			noActive = true
			return nil
		}
		if err != nil {
			return err
		}
		release = askdata.ReleaseRef{
			ReleaseID: askdata.ID(releaseID), ContentHash: askdata.ContentHash(releaseHash),
		}
		rows, err := tx.Query(ctx, `SELECT role.id::text
			FROM platform.user_roles AS assignment
			JOIN platform.roles AS role
			  ON role.tenant_id=assignment.tenant_id AND role.id=assignment.role_id
			WHERE assignment.tenant_id=$1 AND assignment.user_id=$2
			  AND role.status='ACTIVE' AND role.deleted_at IS NULL
			ORDER BY role.id LIMIT $3`, tenantID, actorID, askdata.MaxPolicyRoles+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var roleID string
			if err := rows.Scan(&roleID); err != nil {
				return err
			}
			roleIDs = append(roleIDs, askdata.ID(roleID))
		}
		return rows.Err()
	})
	if err != nil {
		return askdata.PolicyScope{}, "", err
	}
	if noActive {
		return askdata.PolicyScope{}, "NO_ACTIVE_RELEASE", nil
	}
	scope, err := askdata.NewPolicyScope(
		askdata.ID(tenantID), askdata.ID(actorID),
		[]askdata.ID{askdata.ID(domainID)}, roleIDs, release,
	)
	if err != nil {
		return askdata.PolicyScope{}, "", err
	}
	return scope, "", nil
}

// hydrate 把版本命中还原为四分区候选（治理身份 + 名称 + 摘要）。
func (searcher *PostgresVectorSearcher) hydrate(
	ctx context.Context,
	tenantID, domainID string,
	hits []search.RawHit,
) ([]Candidate, error) {
	if len(hits) == 0 {
		return []Candidate{}, nil
	}
	versionIDs := make([]string, 0, len(hits))
	for _, hit := range hits {
		versionIDs = append(versionIDs, string(hit.ObjectVersionID))
	}
	byVersion := map[string]Candidate{}
	err := database.WithTenantTx(ctx, searcher.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT 'MODEL','MODEL',model.model_id::text,model.id::text,
				model.code::text,model.name,model.status,left(model.description,300)
			FROM askdata.semantic_models AS model
			WHERE model.domain_id=$1 AND model.id=ANY($2::uuid[])
			UNION ALL
			SELECT 'METRIC','METRIC',metric.id::text,version.id::text,
				metric.code::text,metric.name,version.status,
				left(CASE WHEN version.business_definition<>'' THEN version.business_definition
					ELSE metric.description END,300)
			FROM askdata.metric_versions AS version
			JOIN askdata.metrics AS metric ON metric.id=version.metric_id
			WHERE version.domain_id=$1 AND version.id=ANY($2::uuid[])
			UNION ALL
			SELECT 'METRIC','MEASURE',measure.measure_id::text,measure.id::text,
				measure.code::text,measure.name,measure.status,left(measure.description,300)
			FROM askdata.measures AS measure
			WHERE measure.domain_id=$1 AND measure.id=ANY($2::uuid[])
			UNION ALL
			SELECT 'DIMENSION','DIMENSION',dimension.dimension_id::text,dimension.id::text,
				dimension.code::text,dimension.name,dimension.status,left(dimension.description,300)
			FROM askdata.dimensions AS dimension
			WHERE dimension.domain_id=$1 AND dimension.id=ANY($2::uuid[])
			UNION ALL
			SELECT 'KNOWLEDGE','KNOWLEDGE',version.business_term_id::text,version.id::text,
				version.code::text,version.name,version.status,left(version.definition,300)
			FROM askdata.business_term_versions AS version
			WHERE version.domain_id=$1 AND version.id=ANY($2::uuid[])`,
			domainID, versionIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var candidate Candidate
			var section string
			if err := rows.Scan(&section, &candidate.ObjectType, &candidate.ObjectID,
				&candidate.VersionID, &candidate.Code, &candidate.Name,
				&candidate.Status, &candidate.Summary); err != nil {
				return err
			}
			candidate.Section = Section(section)
			candidate.Sources = []Source{SourceVector}
			byVersion[candidate.VersionID] = candidate
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	// 保持向量巷道的相似度顺序；未水合的命中（投影落后于注册表）静默跳过。
	result := make([]Candidate, 0, len(hits))
	for _, hit := range hits {
		candidate, exists := byVersion[string(hit.ObjectVersionID)]
		if !exists {
			continue
		}
		candidate.Score = hit.Score
		result = append(result, candidate)
	}
	return result, nil
}

// LineageGraphExpander 沿语义血缘做一跳双向扩展，把直接命中扩展到让它可用
// 的邻居（指标的模型与可用维度、维度的层级、对象的知识词条）。数据集版本
// 与逻辑字段节点不是四分区资产，被跳过。
type LineageGraphExpander struct{ reader lineage.EdgeReader }

func NewLineageGraphExpander(reader lineage.EdgeReader) *LineageGraphExpander {
	return &LineageGraphExpander{reader: reader}
}

var lineageNodeSections = map[lineage.NodeType]struct {
	section    Section
	objectType string
}{
	lineage.NodeModel:     {SectionModel, "MODEL"},
	lineage.NodeMeasure:   {SectionMetric, "MEASURE"},
	lineage.NodeMetric:    {SectionMetric, "METRIC"},
	lineage.NodeDimension: {SectionDimension, "DIMENSION"},
	lineage.NodeHierarchy: {SectionDimension, "HIERARCHY"},
	lineage.NodeKnowledge: {SectionKnowledge, "KNOWLEDGE"},
}

var candidateNodeTypes = map[string]lineage.NodeType{
	"MODEL": lineage.NodeModel, "MEASURE": lineage.NodeMeasure,
	"METRIC": lineage.NodeMetric, "DIMENSION": lineage.NodeDimension,
	"HIERARCHY": lineage.NodeHierarchy, "KNOWLEDGE": lineage.NodeKnowledge,
}

func (expander *LineageGraphExpander) Expand(
	ctx context.Context,
	tenantID, domainID string,
	anchors []Candidate,
	perAnchor int,
) ([]Candidate, error) {
	if expander == nil || expander.reader == nil {
		return nil, ErrUnavailable
	}
	if len(anchors) == 0 || perAnchor < 1 {
		return []Candidate{}, nil
	}
	nodes := make([]lineage.NodeRef, 0, len(anchors))
	anchorCodeByKey := map[string]string{}
	for _, anchor := range anchors {
		nodeType, supported := candidateNodeTypes[anchor.ObjectType]
		if !supported {
			continue
		}
		node := lineage.NodeRef{Type: nodeType, ID: anchor.ObjectID, Code: anchor.Code}
		nodes = append(nodes, node)
		anchorCodeByKey[string(nodeType)+"\x00"+anchor.ObjectID] = anchor.Code
	}
	if len(nodes) == 0 {
		return []Candidate{}, nil
	}
	result := []Candidate{}
	countByAnchor := map[string]int{}
	for _, direction := range []lineage.Direction{lineage.DirectionOut, lineage.DirectionIn} {
		edges, err := expander.reader.AdjacentEdges(
			ctx, tenantID, domainID, nodes, []lineage.Family{lineage.FamilySemantic}, direction,
		)
		if err != nil {
			return nil, err
		}
		for _, edge := range edges {
			anchorNode, neighbour := edge.From, edge.To
			if direction == lineage.DirectionIn {
				anchorNode, neighbour = edge.To, edge.From
			}
			anchorKey := string(anchorNode.Type) + "\x00" + anchorNode.ID
			anchorCode, isAnchor := anchorCodeByKey[anchorKey]
			if !isAnchor || countByAnchor[anchorKey] >= perAnchor {
				continue
			}
			mapping, supported := lineageNodeSections[neighbour.Type]
			if !supported {
				continue
			}
			countByAnchor[anchorKey]++
			result = append(result, Candidate{
				Section: mapping.section, ObjectType: mapping.objectType,
				ObjectID: neighbour.ID, Code: neighbour.Code, Name: neighbour.Code,
				ExpandedFrom: anchorCode,
			})
		}
	}
	return result, nil
}
