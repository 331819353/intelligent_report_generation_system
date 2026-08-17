// Package knowledge adapts the AskData semantic layer — certified business
// terms, governed metrics and dimensions, and certified relationships — into the
// business-knowledge block the dataset AI blueprint turn consumes
// (docs/10 §1.1 A4, §5). It reuses the question-time retrieval stack unchanged
// (dictionary exact hits + hybrid retriever + release-pinned contracts) and adds
// the three identity mappings the modeling side needs:
//
//   - semantic model → dataset version → `dataset-version:<id>` catalog table id;
//   - dimension logical field id / relationship join field id → published field
//     code, which is exactly the column name the version catalog exposes;
//   - measure formula field reference → physical column for metric binding.
//
// Everything is release-scoped and role-scoped through askdata.PolicyScope, so a
// user never sees knowledge the question path would hide from them.
package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/search"
	"intelligent-report-generation-system/internal/askdata/understanding"
	"intelligent-report-generation-system/internal/askdata/understanding/dictionarysearch"
	"intelligent-report-generation-system/internal/datasetai"
	"intelligent-report-generation-system/internal/platform/database"
)

const (
	datasetVersionPrefix = "dataset-version:"
	maxDomains           = 4
	topKPerType          = 8
	maxMention           = 512
)

// Retriever is the hybrid semantic-object retriever (search.Retriever).
type Retriever interface {
	Retrieve(context.Context, search.RetrievalRequest) (search.RetrievalResult, error)
}

// Dictionary resolves certified business vocabulary in free text.
type Dictionary interface {
	Match(context.Context, understanding.DictionaryMatchRequest) (understanding.DictionaryMatchResult, error)
}

// Embedder is optional; without it retrieval runs lexical + exact only.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, string, error)
}

// ContractReader returns release-pinned object contracts (registry.QueryReader).
type ContractReader interface {
	Contracts(ctx context.Context, scope askdata.PolicyScope, domainID askdata.ID, objectVersionIDs []string) ([]registry.ContractRow, error)
}

type Provider struct {
	pool       *pgxpool.Pool
	retriever  Retriever
	dictionary Dictionary
	embedder   Embedder
	reader     ContractReader
	now        func() time.Time
}

// NewProvider wires the AskData stack. pool and reader are required; the
// retriever, dictionary and embedder each degrade gracefully when nil.
func NewProvider(pool *pgxpool.Pool, reader ContractReader, retriever Retriever, dictionary Dictionary, embedder Embedder) (*Provider, error) {
	if pool == nil || reader == nil {
		return nil, errors.New("knowledge provider requires a database pool and a contract reader")
	}
	return &Provider{pool: pool, retriever: retriever, dictionary: dictionary, embedder: embedder, reader: reader, now: time.Now}, nil
}

var _ datasetai.KnowledgeProvider = (*Provider)(nil)

type domainRelease struct {
	DomainID    string
	ReleaseID   string
	ContentHash string
}

// scopeModel is a certified semantic model whose dataset version is one of the
// confirmed scope tables.
type scopeModel struct {
	ModelVersionID   string
	DatasetVersionID string
	TableID          string
	Name             string
	Layer            string
}

// LookupModelingKnowledge implements datasetai.KnowledgeProvider.
func (provider *Provider) LookupModelingKnowledge(ctx context.Context, request datasetai.KnowledgeRequest) (datasetai.BlueprintKnowledge, error) {
	tenantID, actorID := strings.TrimSpace(request.TenantID), strings.TrimSpace(request.ActorID)
	if tenantID == "" || actorID == "" {
		return datasetai.BlueprintKnowledge{}, errors.New("knowledge lookup requires tenant and actor")
	}
	versionIDs := scopeDatasetVersionIDs(request.TableIDs)
	var (
		releases []domainRelease
		roleIDs  []string
		models   []scopeModel
	)
	err := database.WithTenantTx(ctx, provider.pool, tenantID, func(tx pgx.Tx) error {
		var err error
		if roleIDs, err = loadActorRoles(ctx, tx, tenantID, actorID); err != nil {
			return err
		}
		if models, err = loadScopeModels(ctx, tx, tenantID, versionIDs); err != nil {
			return err
		}
		domainIDs := candidateDomains(request.DomainID, models)
		releases, err = loadActiveReleases(ctx, tx, tenantID, domainIDs)
		return err
	})
	if err != nil {
		return datasetai.BlueprintKnowledge{}, err
	}
	if len(roleIDs) == 0 {
		return datasetai.BlueprintKnowledge{Degraded: true, DegradedReason: "ACTOR_HAS_NO_ROLES"}, nil
	}
	if len(releases) == 0 {
		return datasetai.BlueprintKnowledge{Degraded: true, DegradedReason: "NO_ACTIVE_RELEASE"}, nil
	}
	modelsByVersion := map[string]scopeModel{}
	for _, model := range models {
		modelsByVersion[model.ModelVersionID] = model
	}
	result := datasetai.BlueprintKnowledge{}
	mention := truncateRunes(strings.TrimSpace(request.Goal), maxMention)
	var embedding []float32
	embeddingModel := ""
	if provider.embedder != nil && mention != "" {
		vector, model, embedErr := provider.embedder.Embed(ctx, mention)
		if embedErr != nil {
			result.Degraded, result.DegradedReason = true, "EMBEDDING_UNAVAILABLE"
		} else {
			embedding, embeddingModel = vector, model
		}
	}
	for _, release := range releases {
		scope, scopeErr := askdata.NewPolicyScope(
			askdata.ID(tenantID), askdata.ID(actorID),
			[]askdata.ID{askdata.ID(release.DomainID)}, toIDs(roleIDs),
			askdata.ReleaseRef{ReleaseID: askdata.ID(release.ReleaseID), ContentHash: askdata.ContentHash(release.ContentHash)},
		)
		if scopeErr != nil {
			return datasetai.BlueprintKnowledge{}, scopeErr
		}
		domainKnowledge, degradedReason, lookupErr := provider.lookupDomain(ctx, scope, release, mention, embedding, embeddingModel, modelsByVersion)
		if lookupErr != nil {
			slog.WarnContext(ctx, "dataset AI knowledge lookup degraded for domain", "domain_id", release.DomainID, "error", lookupErr)
			result.Degraded, result.DegradedReason = true, "DOMAIN_LOOKUP_FAILED"
			continue
		}
		if degradedReason != "" && !result.Degraded {
			result.Degraded, result.DegradedReason = true, degradedReason
		}
		result.Terms = append(result.Terms, domainKnowledge.Terms...)
		result.Metrics = append(result.Metrics, domainKnowledge.Metrics...)
		result.Dimensions = append(result.Dimensions, domainKnowledge.Dimensions...)
		result.Relationships = append(result.Relationships, domainKnowledge.Relationships...)
	}
	return dedupeKnowledge(result), nil
}

// lookupDomain runs one domain's retrieval under its active release.
func (provider *Provider) lookupDomain(
	ctx context.Context, scope askdata.PolicyScope, release domainRelease, mention string,
	embedding []float32, embeddingModel string, scopeModels map[string]scopeModel,
) (datasetai.BlueprintKnowledge, string, error) {
	result := datasetai.BlueprintKnowledge{}
	degradedReason := ""
	domainID := askdata.ID(release.DomainID)

	// 1. Which semantic objects does the goal talk about? Dictionary exact hits
	//    (certified vocabulary) steer the hybrid retriever.
	candidateIDs := []string{}
	if mention != "" {
		var exact []search.RawHit
		if provider.dictionary != nil {
			match, err := provider.dictionary.Match(ctx, understanding.DictionaryMatchRequest{Scope: scope, Question: mention, Now: provider.now().UTC()})
			if err == nil && len(match.Hits) > 0 {
				if hits, hitErr := dictionarysearch.ExactHits(scope, mention, match.Hits); hitErr == nil {
					exact = hits
				}
			}
		}
		if provider.retriever != nil {
			retrieval, err := provider.retriever.Retrieve(ctx, search.RetrievalRequest{
				Scope: scope, Mention: mention,
				ObjectTypes: []search.ObjectType{search.ObjectMetric, search.ObjectDimension, search.ObjectSemanticModel, search.ObjectBusinessTerm},
				Embedding:   embedding, EmbeddingModel: embeddingModel,
				TopKPerType:        topKPerType,
				DeterministicExact: exact,
			})
			if err != nil {
				return result, "", err
			}
			if retrieval.Degraded {
				degradedReason = retrieval.DegradedReason
			}
			for _, candidate := range retrieval.Candidates {
				candidateIDs = append(candidateIDs, string(candidate.ObjectVersionID))
			}
		} else {
			for _, hit := range exact {
				candidateIDs = append(candidateIDs, string(hit.ObjectVersionID))
			}
			degradedReason = "RETRIEVER_UNAVAILABLE"
		}
	}

	// 2. Every governed dimension of a scope model is relevant regardless of the
	//    goal wording: they tell the blueprint which columns are already governed.
	var scopeDimensions []dimensionRow
	var scopeMetrics []metricBindingRow
	var relationships []relationshipRow
	err := database.WithTenantTx(ctx, provider.pool, string(scope.TenantID), func(tx pgx.Tx) error {
		modelIDs := make([]string, 0, len(scopeModels))
		for id := range scopeModels {
			modelIDs = append(modelIDs, id)
		}
		sort.Strings(modelIDs)
		var err error
		if scopeDimensions, err = loadScopeDimensions(ctx, tx, string(scope.TenantID), release.DomainID, modelIDs); err != nil {
			return err
		}
		if scopeMetrics, err = loadScopeMeasureBindings(ctx, tx, string(scope.TenantID), release.DomainID, modelIDs); err != nil {
			return err
		}
		relationships, err = loadCertifiedRelationships(ctx, tx, string(scope.TenantID), release.DomainID, modelIDs)
		return err
	})
	if err != nil {
		return result, "", err
	}
	for _, dimension := range scopeDimensions {
		model := scopeModels[dimension.ModelVersionID]
		result.Dimensions = append(result.Dimensions, datasetai.KnowledgeDimension{
			Code: dimension.Code, Name: dimension.Name, TableID: model.TableID, Column: dimension.LogicalFieldID,
		})
	}
	for _, relationship := range relationships {
		left, leftOK := scopeModels[relationship.LeftModelVersionID]
		right, rightOK := scopeModels[relationship.RightModelVersionID]
		if !leftOK || !rightOK {
			continue
		}
		key, ok := joinKeyFromAST(relationship.JoinAST)
		if !ok {
			continue
		}
		result.Relationships = append(result.Relationships, datasetai.KnowledgeRelationship{
			LeftTableID: left.TableID, RightTableID: right.TableID,
			JoinType: normalizeJoinType(relationship.JoinType), Cardinality: relationship.Cardinality,
			Keys: []datasetai.JoinKey{key},
		})
	}

	// 3. Release-pinned contracts for the retrieved objects: metrics, terms.
	metricBindingByModelMeasure := map[string]metricBindingRow{}
	for _, binding := range scopeMetrics {
		metricBindingByModelMeasure[binding.MeasureVersionID] = binding
	}
	if len(candidateIDs) > 0 {
		contracts, err := provider.reader.Contracts(ctx, scope, domainID, uniqueStrings(candidateIDs))
		if err != nil {
			return result, "", err
		}
		metricRows := []registry.ContractRow{}
		for _, row := range contracts {
			switch row.ObjectType {
			case "BUSINESS_TERM":
				var term businessTermContract
				if json.Unmarshal(row.Contract, &term) == nil && term.Term != "" {
					result.Terms = append(result.Terms, datasetai.KnowledgeTerm{
						Term: term.Term, Aliases: term.Aliases, Definition: term.Definition,
						TargetType: term.TargetObjectType, TargetCode: term.TargetCode,
					})
				}
			case "METRIC":
				metricRows = append(metricRows, row)
			}
		}
		if len(metricRows) > 0 {
			// Metric names live on the metric identity row, aggregation on the
			// measure; both are read once for the whole batch.
			metricIDs := make([]string, 0, len(metricRows))
			measureIDs := []string{}
			parsed := make([]metricContract, 0, len(metricRows))
			for _, row := range metricRows {
				var metric metricContract
				if json.Unmarshal(row.Contract, &metric) != nil {
					continue
				}
				metric.versionID = row.ObjectVersionID
				metricIDs = append(metricIDs, metric.MetricID)
				measureIDs = append(measureIDs, metric.MeasureVersionIDs...)
				parsed = append(parsed, metric)
			}
			var names map[string]metricIdentity
			var measures map[string]measureContract
			err := database.WithTenantTx(ctx, provider.pool, string(scope.TenantID), func(tx pgx.Tx) error {
				var err error
				if names, err = loadMetricIdentities(ctx, tx, string(scope.TenantID), uniqueStrings(metricIDs)); err != nil {
					return err
				}
				measures, err = loadMeasures(ctx, tx, string(scope.TenantID), uniqueStrings(measureIDs))
				return err
			})
			if err != nil {
				return result, "", err
			}
			for _, metric := range parsed {
				identity := names[metric.MetricID]
				item := datasetai.KnowledgeMetric{
					Code: identity.Code, Name: firstNonEmpty(identity.Name, metric.Name),
					BusinessDefinition: firstNonEmpty(metric.BusinessDefinition, metric.Definition, identity.Description),
					TimeGrain:          metric.TimeGrain, Additivity: string(metric.Additivity),
				}
				if item.Code == "" {
					item.Code = metric.MetricID
				}
				for _, measureID := range metric.MeasureVersionIDs {
					measure, ok := measures[measureID]
					if !ok {
						continue
					}
					if item.Aggregation == "" {
						item.Aggregation = measure.Aggregation
					}
					if binding, ok := metricBindingByModelMeasure[measureID]; ok && item.TableID == "" {
						item.TableID, item.Column = binding.TableID, binding.Column
					} else if model, ok := scopeModels[measure.SemanticModelVersionID]; ok && item.TableID == "" {
						if column := formulaFieldID(measure.FormulaAST); column != "" {
							item.TableID, item.Column = model.TableID, column
						}
					}
				}
				result.Metrics = append(result.Metrics, item)
			}
		}
	}
	return result, degradedReason, nil
}

// --- contract shapes (subset of registry/object_contract.go documents) ---

type businessTermContract struct {
	Term             string   `json:"term"`
	TargetObjectType string   `json:"targetObjectType"`
	TargetCode       string   `json:"targetCode"`
	Definition       string   `json:"definition"`
	Aliases          []string `json:"aliases"`
}

type metricContract struct {
	MetricID           string   `json:"metricId"`
	Name               string   `json:"name"`
	Definition         string   `json:"definition"`
	BusinessDefinition string   `json:"businessDefinition"`
	TimeGrain          string   `json:"timeGrain"`
	Additivity         string   `json:"additivity"`
	MeasureVersionIDs  []string `json:"measureVersionIds"`
	versionID          string
}

type measureContract struct {
	SemanticModelVersionID string          `json:"semanticModelVersionId"`
	Aggregation            string          `json:"aggregation"`
	FormulaAST             json.RawMessage `json:"formulaAst"`
}

type metricIdentity struct {
	Code        string
	Name        string
	Description string
}

type dimensionRow struct {
	ModelVersionID string
	Code           string
	Name           string
	LogicalFieldID string
}

type metricBindingRow struct {
	MeasureVersionID string
	TableID          string
	Column           string
}

type relationshipRow struct {
	LeftModelVersionID  string
	RightModelVersionID string
	JoinType            string
	Cardinality         string
	JoinAST             json.RawMessage
}

// --- SQL readers (all under the tenant transaction) ---

func loadActorRoles(ctx context.Context, tx pgx.Tx, tenantID, actorID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT role.id::text FROM platform.user_roles AS assignment
		JOIN platform.roles AS role ON role.tenant_id=assignment.tenant_id AND role.id=assignment.role_id
		WHERE assignment.tenant_id=$1::uuid AND assignment.user_id=$2::uuid
		  AND role.status='ACTIVE' AND role.deleted_at IS NULL ORDER BY role.id LIMIT $3`,
		tenantID, actorID, askdata.MaxPolicyRoles)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func loadScopeModels(ctx context.Context, tx pgx.Tx, tenantID string, versionIDs []string) ([]scopeModel, error) {
	if len(versionIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT model.id::text,model.dataset_version_id::text,model.name,model.layer
		FROM askdata.semantic_models AS model
		WHERE model.tenant_id=$1::uuid AND model.status='CERTIFIED'
		  AND model.dataset_version_id::text=ANY($2)
		ORDER BY model.dataset_version_id,model.version_no DESC`, tenantID, versionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []scopeModel{}
	seenDatasetVersion := map[string]bool{}
	for rows.Next() {
		var model scopeModel
		if err := rows.Scan(&model.ModelVersionID, &model.DatasetVersionID, &model.Name, &model.Layer); err != nil {
			return nil, err
		}
		model.TableID = datasetVersionPrefix + model.DatasetVersionID
		// One model per dataset version: the newest certified one.
		if seenDatasetVersion[model.DatasetVersionID] {
			continue
		}
		seenDatasetVersion[model.DatasetVersionID] = true
		result = append(result, model)
	}
	return result, rows.Err()
}

func loadActiveReleases(ctx context.Context, tx pgx.Tx, tenantID string, domainIDs []string) ([]domainRelease, error) {
	query := `SELECT release.domain_id::text,release.id::text,release.content_hash
		FROM askdata.releases AS release
		WHERE release.tenant_id=$1::uuid AND release.status='ACTIVE'`
	args := []any{tenantID}
	if len(domainIDs) > 0 {
		query += ` AND release.domain_id::text=ANY($2)`
		args = append(args, domainIDs)
	}
	query += fmt.Sprintf(` ORDER BY release.activated_at DESC NULLS LAST,release.domain_id LIMIT %d`, maxDomains)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domainRelease{}
	seen := map[string]bool{}
	for rows.Next() {
		var release domainRelease
		if err := rows.Scan(&release.DomainID, &release.ReleaseID, &release.ContentHash); err != nil {
			return nil, err
		}
		if seen[release.DomainID] {
			continue
		}
		seen[release.DomainID] = true
		result = append(result, release)
	}
	return result, rows.Err()
}

func loadScopeDimensions(ctx context.Context, tx pgx.Tx, tenantID, domainID string, modelIDs []string) ([]dimensionRow, error) {
	if len(modelIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT dimension.semantic_model_version_id::text,dimension.code,dimension.name,dimension.logical_field_id
		FROM askdata.dimensions AS dimension
		WHERE dimension.tenant_id=$1::uuid AND dimension.domain_id=$2::uuid AND dimension.status='CERTIFIED'
		  AND dimension.sensitivity<>'RESTRICTED'
		  AND dimension.semantic_model_version_id::text=ANY($3)
		ORDER BY dimension.semantic_model_version_id,dimension.code`, tenantID, domainID, modelIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []dimensionRow{}
	for rows.Next() {
		var row dimensionRow
		if err := rows.Scan(&row.ModelVersionID, &row.Code, &row.Name, &row.LogicalFieldID); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// loadScopeMeasureBindings resolves each certified measure of a scope model to
// the physical column its formula references, when the formula is a plain field.
func loadScopeMeasureBindings(ctx context.Context, tx pgx.Tx, tenantID, domainID string, modelIDs []string) ([]metricBindingRow, error) {
	if len(modelIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT measure.id::text,measure.semantic_model_version_id::text,model.dataset_version_id::text,measure.formula_ast
		FROM askdata.measures AS measure
		JOIN askdata.semantic_models AS model ON model.id=measure.semantic_model_version_id AND model.tenant_id=measure.tenant_id
		WHERE measure.tenant_id=$1::uuid AND measure.domain_id=$2::uuid AND measure.status='CERTIFIED'
		  AND measure.semantic_model_version_id::text=ANY($3)`, tenantID, domainID, modelIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []metricBindingRow{}
	for rows.Next() {
		var measureID, modelID, datasetVersionID string
		var formula json.RawMessage
		if err := rows.Scan(&measureID, &modelID, &datasetVersionID, &formula); err != nil {
			return nil, err
		}
		if column := formulaFieldID(formula); column != "" {
			result = append(result, metricBindingRow{MeasureVersionID: measureID, TableID: datasetVersionPrefix + datasetVersionID, Column: column})
		}
	}
	return result, rows.Err()
}

func loadCertifiedRelationships(ctx context.Context, tx pgx.Tx, tenantID, domainID string, modelIDs []string) ([]relationshipRow, error) {
	if len(modelIDs) < 2 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT relationship.left_model_version_id::text,relationship.right_model_version_id::text,
		       relationship.join_type,relationship.cardinality,relationship.join_ast
		FROM askdata.relationships AS relationship
		WHERE relationship.tenant_id=$1::uuid AND relationship.domain_id=$2::uuid AND relationship.status='CERTIFIED'
		  AND relationship.relationship_type='MODEL_JOIN' AND relationship.join_type<>'NONE'
		  AND relationship.left_model_version_id::text=ANY($3)
		  AND relationship.right_model_version_id::text=ANY($3)
		ORDER BY relationship.left_model_version_id,relationship.right_model_version_id,relationship.version_no DESC`, tenantID, domainID, modelIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []relationshipRow{}
	seen := map[string]bool{}
	for rows.Next() {
		var row relationshipRow
		if err := rows.Scan(&row.LeftModelVersionID, &row.RightModelVersionID, &row.JoinType, &row.Cardinality, &row.JoinAST); err != nil {
			return nil, err
		}
		key := row.LeftModelVersionID + "\x00" + row.RightModelVersionID
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, row)
	}
	return result, rows.Err()
}

func loadMetricIdentities(ctx context.Context, tx pgx.Tx, tenantID string, metricIDs []string) (map[string]metricIdentity, error) {
	result := map[string]metricIdentity{}
	if len(metricIDs) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT metric.id::text,metric.code,metric.name,metric.description
		FROM askdata.metrics AS metric WHERE metric.tenant_id=$1::uuid AND metric.id::text=ANY($2)`, tenantID, metricIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var identity metricIdentity
		if err := rows.Scan(&id, &identity.Code, &identity.Name, &identity.Description); err != nil {
			return nil, err
		}
		result[id] = identity
	}
	return result, rows.Err()
}

func loadMeasures(ctx context.Context, tx pgx.Tx, tenantID string, measureIDs []string) (map[string]measureContract, error) {
	result := map[string]measureContract{}
	if len(measureIDs) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT measure.id::text,measure.semantic_model_version_id::text,measure.aggregation,measure.formula_ast
		FROM askdata.measures AS measure WHERE measure.tenant_id=$1::uuid AND measure.id::text=ANY($2)`, tenantID, measureIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var measure measureContract
		if err := rows.Scan(&id, &measure.SemanticModelVersionID, &measure.Aggregation, &measure.FormulaAST); err != nil {
			return nil, err
		}
		result[id] = measure
	}
	return result, rows.Err()
}

// --- pure mapping helpers ---

// scopeDatasetVersionIDs extracts the dataset version ids behind
// `dataset-version:<id>` catalog ids; physical tables carry no semantic layer.
func scopeDatasetVersionIDs(tableIDs []string) []string {
	result := []string{}
	for _, tableID := range tableIDs {
		if strings.HasPrefix(tableID, datasetVersionPrefix) {
			if id := strings.TrimPrefix(tableID, datasetVersionPrefix); id != "" {
				result = append(result, id)
			}
		}
	}
	return result
}

// candidateDomains prefers the session's domain, then the domains of the scope
// models; empty means "every active release of the tenant" (bounded).
func candidateDomains(explicit string, models []scopeModel) []string {
	if strings.TrimSpace(explicit) != "" {
		return []string{strings.TrimSpace(explicit)}
	}
	_ = models // scope models do not carry a domain column; releases decide.
	return nil
}

// joinAST is the certified relationship join contract (compiler/join.go).
type joinAST struct {
	Type         string `json:"type"`
	LeftFieldID  string `json:"leftFieldId"`
	RightFieldID string `json:"rightFieldId"`
}

// joinKeyFromAST maps a relationship join AST to a blueprint join key. Field ids
// in the semantic registry are the published field codes, i.e. the version
// catalog's column names, so no further lookup is needed.
func joinKeyFromAST(raw json.RawMessage) (datasetai.JoinKey, bool) {
	var ast joinAST
	if err := json.Unmarshal(raw, &ast); err != nil || ast.Type != "EQUALS" || ast.LeftFieldID == "" || ast.RightFieldID == "" {
		return datasetai.JoinKey{}, false
	}
	return datasetai.JoinKey{LeftColumn: ast.LeftFieldID, RightColumn: ast.RightFieldID}, true
}

// formulaFieldID returns the physical field a measure formula reads when the
// formula is a plain field reference (or an aggregate/wrapper of one).
func formulaFieldID(raw json.RawMessage) string {
	var node struct {
		Type      string            `json:"type"`
		FieldID   string            `json:"fieldId"`
		Argument  json.RawMessage   `json:"argument"`
		Arguments []json.RawMessage `json:"arguments"`
		Left      json.RawMessage   `json:"left"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &node) != nil {
		return ""
	}
	if node.FieldID != "" {
		return node.FieldID
	}
	for _, child := range append([][]byte{node.Argument, node.Left}, toBytes(node.Arguments)...) {
		if len(child) == 0 {
			continue
		}
		if field := formulaFieldID(child); field != "" {
			return field
		}
	}
	return ""
}

func toBytes(values []json.RawMessage) [][]byte {
	result := make([][]byte, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func normalizeJoinType(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "INNER":
		return "INNER"
	case "LEFT", "RIGHT", "FULL":
		// The blueprint expresses joins from the primary side; a certified RIGHT
		// or FULL join is offered as LEFT and the user can flip it on the card.
		return "LEFT"
	}
	return "LEFT"
}

func dedupeKnowledge(value datasetai.BlueprintKnowledge) datasetai.BlueprintKnowledge {
	seenTerms := map[string]bool{}
	terms := value.Terms[:0]
	for _, term := range value.Terms {
		key := strings.ToLower(term.Term) + "\x00" + term.TargetCode
		if seenTerms[key] {
			continue
		}
		seenTerms[key] = true
		terms = append(terms, term)
	}
	value.Terms = terms
	seenMetrics := map[string]bool{}
	metrics := value.Metrics[:0]
	for _, metric := range value.Metrics {
		if seenMetrics[metric.Code] {
			continue
		}
		seenMetrics[metric.Code] = true
		metrics = append(metrics, metric)
	}
	value.Metrics = metrics
	seenDimensions := map[string]bool{}
	dimensions := value.Dimensions[:0]
	for _, dimension := range value.Dimensions {
		key := dimension.TableID + "\x00" + dimension.Column
		if seenDimensions[key] {
			continue
		}
		seenDimensions[key] = true
		dimensions = append(dimensions, dimension)
	}
	value.Dimensions = dimensions
	seenRelationships := map[string]bool{}
	relationships := value.Relationships[:0]
	for _, relationship := range value.Relationships {
		key := relationship.LeftTableID + "\x00" + relationship.RightTableID
		if seenRelationships[key] {
			continue
		}
		seenRelationships[key] = true
		relationships = append(relationships, relationship)
	}
	value.Relationships = relationships
	return value
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func toIDs(values []string) []askdata.ID {
	result := make([]askdata.ID, 0, len(values))
	for _, value := range values {
		result = append(result, askdata.ID(value))
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
