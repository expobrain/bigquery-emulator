package contentdata

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	internaltypes "github.com/goccy/bigquery-emulator/internal/types"
)

var (
	createSnapshotTablePattern = regexp.MustCompile(
		`(?is)^\s*CREATE\s+SNAPSHOT\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([^\s]+)\s+CLONE\s+([^\s]+)\s*;?\s*$`,
	)
	createCloneTablePattern = regexp.MustCompile(
		`(?is)^\s*CREATE\s+(?:OR\s+REPLACE\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([^\s]+)\s+CLONE\s+([^\s]+)\s*;?\s*$`,
	)
	dropSnapshotTablePattern = regexp.MustCompile(
		`(?is)^\s*DROP\s+SNAPSHOT\s+TABLE\s+(IF\s+EXISTS\s+)?([^\s]+)\s*;?\s*$`,
	)
	dropTablePattern = regexp.MustCompile(
		`(?is)^\s*DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?([^\s]+)\s*;?\s*$`,
	)
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

// ResolveTableRef resolves a table reference against the query's defaults.
func ResolveTableRef(ref, defaultProject, defaultDataset string) (projectID, datasetID, tableID string) {
	return splitTableReference(ref, defaultProject, defaultDataset)
}

// PlainDropTableRef returns the table reference of a DROP TABLE statement, so
// callers that own table metadata can reject drops of snapshots (which must
// use DROP SNAPSHOT TABLE).
func PlainDropTableRef(query string) (string, bool) {
	match := dropTablePattern.FindStringSubmatch(query)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// rewriteDropSnapshotTable converts BigQuery's DROP SNAPSHOT TABLE statement
// into the DROP TABLE form understood by googlesqlite. The underlying table
// holds the snapshot's materialised data, so dropping it removes both the data
// and (via the catalog sync in the handler) its metadata.
func rewriteDropSnapshotTable(query string) string {
	match := dropSnapshotTablePattern.FindStringSubmatch(query)
	if match == nil {
		return query
	}
	ifExists := ""
	if match[1] != "" {
		ifExists = " IF EXISTS"
	}
	return fmt.Sprintf("DROP TABLE%s %s", ifExists, match[2])
}

// rewriteCloneDDL converts BigQuery's CLONE DDL statements into an equivalent
// CREATE TABLE ... AS SELECT. The materialised copy keeps the resulting table
// stable when the source is later modified or dropped. The returned definition
// is applied to the table metadata by the handler after the catalog has been
// synchronised. Snapshots carry a SNAPSHOT kind (and table type); ordinary
// clones stay typed TABLE and only add a cloneDefinition.
func rewriteCloneDDL(query, defaultProject, defaultDataset string) (string, *internaltypes.TableDefinition) {
	match := createSnapshotTablePattern.FindStringSubmatch(query)
	kind := "SNAPSHOT"
	if match == nil {
		match = createCloneTablePattern.FindStringSubmatch(query)
		kind = "CLONE"
	}
	if match == nil {
		return query, nil
	}
	destRef, sourceRef := match[1], match[2]
	projectID, datasetID, tableID := splitTableReference(destRef, defaultProject, defaultDataset)
	baseProjectID, baseDatasetID, baseTableID := splitTableReference(sourceRef, defaultProject, defaultDataset)
	return fmt.Sprintf("CREATE TABLE %s AS SELECT * FROM %s", destRef, sourceRef), &internaltypes.TableDefinition{
		Kind:           kind,
		ProjectID:      projectID,
		DatasetID:      datasetID,
		TableID:        tableID,
		BaseProjectID:  baseProjectID,
		BaseDatasetID:  baseDatasetID,
		BaseTableID:    baseTableID,
		DefinitionTime: time.Now().UTC(),
	}
}
