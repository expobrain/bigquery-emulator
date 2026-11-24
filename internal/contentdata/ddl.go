package contentdata

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	internaltypes "github.com/goccy/bigquery-emulator/internal/types"
)

var createSnapshotTablePattern = regexp.MustCompile(
	`(?is)^\s*CREATE\s+SNAPSHOT\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([^\s]+)\s+CLONE\s+([^\s]+)\s*;?\s*$`,
)

// splitTableReference resolves a (possibly qualified) table reference against
// the query's default project and dataset.
func splitTableReference(ref, defaultProject, defaultDataset string) (projectID, datasetID, tableID string) {
	ref = strings.TrimSpace(ref)
	if len(ref) >= 2 && ref[0] == '`' && ref[len(ref)-1] == '`' {
		ref = ref[1 : len(ref)-1]
	}
	parts := strings.Split(ref, ".")
	switch len(parts) {
	case 3:
		return parts[0], parts[1], parts[2]
	case 2:
		return defaultProject, parts[0], parts[1]
	default:
		return defaultProject, defaultDataset, parts[0]
	}
}

// rewriteCreateSnapshotTable converts a BigQuery CREATE SNAPSHOT TABLE ...
// CLONE statement into an equivalent CREATE TABLE ... AS SELECT. The
// materialised copy keeps the snapshot stable when the source is later
// modified or dropped. The returned definition is applied to the table
// metadata by the handler after the catalog has been synchronised.
func rewriteCreateSnapshotTable(query, defaultProject, defaultDataset string) (string, *internaltypes.TableDefinition) {
	match := createSnapshotTablePattern.FindStringSubmatch(query)
	if match == nil {
		return query, nil
	}
	destRef, sourceRef := match[1], match[2]
	projectID, datasetID, tableID := splitTableReference(destRef, defaultProject, defaultDataset)
	baseProjectID, baseDatasetID, baseTableID := splitTableReference(sourceRef, defaultProject, defaultDataset)
	return fmt.Sprintf("CREATE TABLE %s AS SELECT * FROM %s", destRef, sourceRef), &internaltypes.TableDefinition{
		Kind:           "SNAPSHOT",
		ProjectID:      projectID,
		DatasetID:      datasetID,
		TableID:        tableID,
		BaseProjectID:  baseProjectID,
		BaseDatasetID:  baseDatasetID,
		BaseTableID:    baseTableID,
		DefinitionTime: time.Now().UTC(),
	}
}
