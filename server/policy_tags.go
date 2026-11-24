package server

import (
	"net/http"
	"regexp"
	"strings"

	bigqueryv2 "google.golang.org/api/bigquery/v2"

	"github.com/goccy/bigquery-emulator/internal/metadata"
)

// Policy-tag caller model
// -----------------------
// The emulator has no authentication principal. A caller declares the policy
// tags it may access with the X-BigQuery-Allowed-Policy-Tags request header,
// whose value is a comma-separated list of fully qualified tag names (the
// values used in TableFieldSchema.PolicyTags.Names). The default is an empty
// set: callers cannot read any tagged column unless they opt in explicitly.
//
// Enforcement is intentionally conservative: a query that mentions a tagged
// column name as a word is rejected unless every tag on that column is
// allowed. This covers jobs.query and jobs.insert, including dry runs, before
// any rows are returned. It can produce false positives when the same name is
// used in another table; the header provides an explicit escape hatch.
const allowedPolicyTagsHeader = "X-BigQuery-Allowed-Policy-Tags"

func allowedPolicyTagsFromRequest(r *http.Request) map[string]bool {
	allowed := map[string]bool{}
	value := r.Header.Get(allowedPolicyTagsHeader)
	for _, tag := range strings.Split(value, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			allowed[tag] = true
		}
	}
	return allowed
}

// identifierPattern matches a column name as a standalone word so that
// `ssn` is not considered read by a column named `ssn_hash`.
func identifierPattern(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])` + regexp.QuoteMeta(name) + `(?:$|[^A-Za-z0-9_])`)
}

// checkPolicyTags rejects reads of columns whose policy tags are not allowed
// for this caller. Only tags declared in table metadata are considered.
func checkPolicyTags(project *metadata.Project, query string, allowed map[string]bool) error {
	if project == nil {
		return nil
	}
	for _, datasetID := range project.DatasetIDs() {
		dataset := project.Dataset(datasetID)
		if dataset == nil {
			continue
		}
		for _, table := range dataset.Tables() {
			content, err := table.Content()
			if err != nil || content.Schema == nil {
				continue
			}
			for _, field := range content.Schema.Fields {
				if err := checkFieldPolicyTags(field, query, allowed); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func checkFieldPolicyTags(field *bigqueryv2.TableFieldSchema, query string, allowed map[string]bool) error {
	if field == nil {
		return nil
	}
	if field.PolicyTags != nil {
		for _, tag := range field.PolicyTags.Names {
			if allowed[tag] {
				continue
			}
			if identifierPattern(field.Name).MatchString(query) {
				return errAccessDenied("access denied to policy protected column " + field.Name)
			}
		}
	}
	for _, child := range field.Fields {
		if err := checkFieldPolicyTags(child, query, allowed); err != nil {
			return err
		}
	}
	return nil
}
