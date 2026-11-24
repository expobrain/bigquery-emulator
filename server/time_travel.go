package server

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/goccy/bigquery-emulator/internal/contentdata"
	"github.com/goccy/bigquery-emulator/internal/metadata"
)

// Time travel in this emulator is snapshot-backed: a query of the form
//
//	SELECT * FROM dataset.table FOR SYSTEM_TIME AS OF TIMESTAMP('...')
//
// is resolved to the most recent snapshot of that table taken at or before
// the requested time. Snapshots are retained until they are explicitly
// dropped with DROP SNAPSHOT TABLE; there is no background cleanup. This is
// deliberately simpler than BigQuery's write-time versioning: it keeps the
// storage model small but means only points with an explicit snapshot can be
// queried. The dataset's max_time_travel_hours (when set) bounds how old a
// requested point may be.
var timeTravelPattern = regexp.MustCompile(
	"(?is)(FROM\\s+)(`?[\\w.\\-]+`?)\\s+FOR\\s+SYSTEM_TIME\\s+AS\\s+OF\\s+(?:TIMESTAMP\\s*\\(\\s*)?'([^']+)'(?:\\s*\\))?",
)

// applyTimeTravel rewrites FOR SYSTEM_TIME AS OF clauses to read from the
// retained snapshot that was closest to (but not after) the requested time.
func applyTimeTravel(ctx context.Context, server *Server, query, defaultProject, defaultDataset string) (string, error) {
	if !strings.Contains(strings.ToUpper(query), "FOR SYSTEM_TIME AS OF") {
		return query, nil
	}
	var rewriteErr error
	rewritten := timeTravelPattern.ReplaceAllStringFunc(query, func(match string) string {
		if rewriteErr != nil {
			return match
		}
		groups := timeTravelPattern.FindStringSubmatch(match)
		if len(groups) != 4 {
			rewriteErr = errInvalidQuery("invalid FOR SYSTEM_TIME AS OF clause")
			return match
		}
		asOf, err := parseTimeTravelTimestamp(groups[3])
		if err != nil {
			rewriteErr = errInvalidQuery(fmt.Sprintf("invalid time travel timestamp %q", groups[3]))
			return match
		}
		ref := strings.Trim(groups[2], "`")
		projectID, datasetID, tableID := contentdata.ResolveTableRef(ref, defaultProject, defaultDataset)
		project, err := server.metaRepo.FindProject(ctx, projectID)
		if err != nil || project == nil {
			rewriteErr = errInvalidQuery(fmt.Sprintf("project %s is not found", projectID))
			return match
		}
		dataset := project.Dataset(datasetID)
		if dataset == nil {
			rewriteErr = errInvalidQuery(fmt.Sprintf("dataset %s is not found", datasetID))
			return match
		}
		if maxHours := int64(dataset.Content().MaxTimeTravelHours); maxHours > 0 && asOf.Before(time.Now().Add(-time.Duration(maxHours)*time.Hour)) {
			rewriteErr = errInvalidQuery(fmt.Sprintf("requested time is older than the %d hour retention window", maxHours))
			return match
		}
		snapshotID, ok := nearestSnapshot(dataset, projectID, datasetID, tableID, asOf)
		if !ok {
			rewriteErr = errInvalidQuery(fmt.Sprintf("no retained snapshot of %s.%s at or before %s", datasetID, tableID, asOf.Format(time.RFC3339Nano)))
			return match
		}
		replacement := snapshotID
		switch len(strings.Split(ref, ".")) {
		case 2:
			replacement = datasetID + "." + snapshotID
		case 3:
			replacement = projectID + "." + datasetID + "." + snapshotID
		}
		if strings.HasPrefix(groups[2], "`") {
			replacement = "`" + replacement + "`"
		}
		return groups[1] + replacement
	})
	if rewriteErr != nil {
		return "", rewriteErr
	}
	return rewritten, nil
}

func parseTimeTravelTimestamp(s string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp %q", s)
}

// nearestSnapshot returns the newest snapshot of the given base table whose
// snapshot time is at or before the requested point.
func nearestSnapshot(dataset *metadata.Dataset, projectID, datasetID, tableID string, asOf time.Time) (string, bool) {
	var bestID string
	var bestTime time.Time
	for _, table := range dataset.Tables() {
		if !table.IsSnapshot() {
			continue
		}
		content, err := table.Content()
		if err != nil || content.SnapshotDefinition == nil || content.SnapshotDefinition.BaseTableReference == nil {
			continue
		}
		base := content.SnapshotDefinition.BaseTableReference
		if base.TableId != tableID || base.DatasetId != datasetID || base.ProjectId != projectID {
			continue
		}
		snapshotTime, err := time.Parse(time.RFC3339Nano, content.SnapshotDefinition.SnapshotTime)
		if err != nil || snapshotTime.After(asOf) {
			continue
		}
		if bestID == "" || snapshotTime.After(bestTime) {
			bestID = table.ID
			bestTime = snapshotTime
		}
	}
	return bestID, bestID != ""
}
