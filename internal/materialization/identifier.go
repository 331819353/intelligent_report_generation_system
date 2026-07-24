package materialization

import (
	"fmt"
	"regexp"
	"strings"
)

var physicalNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// GeneratePhysicalIdentifier returns names owned by the platform, not callers.
// Hash fragments keep identifiers below PostgreSQL's 63-byte limit and avoid
// exposing raw tenant or dataset UUIDs in catalog listings.
func GeneratePhysicalIdentifier(tenantID, datasetID, runID string, layer Layer) (PhysicalIdentifier, error) {
	if !validUUID(tenantID) || !validUUID(datasetID) || !validUUID(runID) || !validLayer(layer) {
		return PhysicalIdentifier{}, ErrInvalidRequest
	}
	schema := ""
	prefix := strings.ToLower(string(layer))
	switch layer {
	case LayerODS:
		schema = "warehouse_ods"
	case LayerDWD:
		schema = "warehouse_dwd"
	case LayerDWS:
		schema = "warehouse_dws"
	}
	tenantHash := sha256Hex([]byte("tenant\x00" + tenantID))[:12]
	datasetHash := sha256Hex([]byte("dataset\x00" + datasetID))[:12]
	runHash := sha256Hex([]byte("run\x00" + runID))[:12]
	return PhysicalIdentifier{
		Schema:          schema,
		Name:            fmt.Sprintf("%s_t%s_d%s_r%s", prefix, tenantHash, datasetHash, runHash),
		PublishedSchema: "warehouse_published",
		PublishedName:   fmt.Sprintf("%s_t%s_d%s", prefix, tenantHash, datasetHash),
	}, nil
}

// GenerateStagingIdentifier returns a run/node-scoped name. The node ID is
// hashed rather than interpolated, so even an internal caller cannot inject an
// identifier or cross the warehouse_staging schema boundary.
func GenerateStagingIdentifier(tenantID, runID, nodeID string) (schema, name string, err error) {
	if !validUUID(tenantID) || !validUUID(runID) || !nodeIDPattern.MatchString(nodeID) {
		return "", "", ErrInvalidRequest
	}
	tenantHash := sha256Hex([]byte("tenant\x00" + tenantID))[:12]
	runHash := sha256Hex([]byte("run\x00" + runID))[:12]
	nodeHash := sha256Hex([]byte("node\x00" + nodeID))[:12]
	return "warehouse_staging", fmt.Sprintf("stage_t%s_r%s_n%s", tenantHash, runHash, nodeHash), nil
}

func ValidatePhysicalIdentifier(identifier PhysicalIdentifier, tenantID, datasetID, runID string, layer Layer) error {
	if !physicalNamePattern.MatchString(identifier.Name) ||
		!physicalNamePattern.MatchString(identifier.PublishedName) {
		return ErrInvalidRequest
	}
	expected, err := GeneratePhysicalIdentifier(tenantID, datasetID, runID, layer)
	if err != nil {
		return err
	}
	if identifier != expected {
		return ErrInvalidRequest
	}
	return nil
}

// publicationSwapNames derives transaction-local and retired view names without
// interpolating run IDs. The stable published name remains unchanged; old views
// are retained outside warehouse_published so incompatible schema changes do
// not require DROP ... CASCADE and are no longer reachable by the API role.
func publicationSwapNames(identifier PhysicalIdentifier, currentRunID, previousRunID string) (next, retired string, err error) {
	if !physicalNamePattern.MatchString(identifier.PublishedName) ||
		!validUUID(currentRunID) ||
		(previousRunID != "" && !validUUID(previousRunID)) {
		return "", "", ErrInvalidRequest
	}
	next = identifier.PublishedName + "_n" + sha256Hex([]byte("next\x00" + currentRunID))[:12]
	if previousRunID != "" {
		retired = identifier.PublishedName + "_r" + sha256Hex([]byte("retired\x00" + previousRunID))[:12]
	}
	if !physicalNamePattern.MatchString(next) ||
		(retired != "" && !physicalNamePattern.MatchString(retired)) {
		return "", "", ErrInvalidRequest
	}
	return next, retired, nil
}
