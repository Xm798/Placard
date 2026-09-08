package authz

import (
	"github.com/Xm798/placard/internal/model"
)

// View decides whether authzID may view file, per the visibility judgement
// order fixed by spec §5 (the order IS the security invariant, do not
// reorder):
//
//	owner short-circuit → link allow → private deny → unknown value
//	default-deny.
//
// Auth is by authz_id string comparison ONLY — this never joins the user table;
// an anonymous caller (authzID == "") can only ever pass the link branch.
func View(file *model.File, authzID string) bool {
	if authzID != "" && authzID == file.CreateUser {
		return true
	}
	switch file.Visibility {
	case model.VisibilityLink:
		return true
	case model.VisibilityPrivate:
		return false
	default:
		// Unknown/legacy visibility value: fail closed rather than assume
		// permissive (defends against a future enum value the server doesn't
		// know about yet, or corrupted data).
		return false
	}
}
