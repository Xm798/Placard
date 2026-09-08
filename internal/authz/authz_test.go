package authz

import (
	"testing"

	"github.com/Xm798/placard/internal/model"
)

const (
	authzOwner     = "owner"
	authzOtherUser = "stranger"
)

func TestAuthorizeView(t *testing.T) {
	cases := []struct {
		name       string
		visibility string
		authzID    string
		want       bool
	}{
		{"link owner", model.VisibilityLink, authzOwner, true},
		{"link other user", model.VisibilityLink, authzOtherUser, true},
		{"link anonymous", model.VisibilityLink, "", true},

		{"private owner", model.VisibilityPrivate, authzOwner, true},
		{"private other user", model.VisibilityPrivate, authzOtherUser, false},
		{"private anonymous", model.VisibilityPrivate, "", false},

		// "restricted" is no longer a defined value: it must fall through to
		// the default-deny branch like any other unknown string.
		{"restricted owner still allowed", "restricted", authzOwner, true},
		{"restricted non-owner default-deny", "restricted", authzOtherUser, false},
		{"restricted anonymous default-deny", "restricted", "", false},

		{"unknown value owner still allowed", "public", authzOwner, true},
		{"unknown value non-owner default-deny", "public", authzOtherUser, false},
		{"unknown value anonymous default-deny", "public", "", false},
		{"empty visibility non-owner default-deny", "", authzOtherUser, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file := &model.File{NanoID: "abc12345", CreateUser: authzOwner, Visibility: tc.visibility}
			if got := View(file, tc.authzID); got != tc.want {
				t.Errorf("View(visibility=%q, authzID=%q) = %v, want %v",
					tc.visibility, tc.authzID, got, tc.want)
			}
		})
	}
}

// An empty authz id must never match an empty CreateUser through the owner
// short-circuit — an ownerless row would otherwise be readable by every
// anonymous caller regardless of visibility.
func TestAuthorizeViewAnonymousNeverMatchesEmptyOwner(t *testing.T) {
	file := &model.File{NanoID: "abc12345", CreateUser: "", Visibility: model.VisibilityPrivate}
	if View(file, "") {
		t.Error("anonymous caller matched an empty CreateUser")
	}
}
