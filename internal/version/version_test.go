package version

import "testing"

func TestPublic(t *testing.T) {
	originalVersion, originalCommit, originalBranch := Version, GitCommit, GitBranch
	t.Cleanup(func() {
		Version, GitCommit, GitBranch = originalVersion, originalCommit, originalBranch
	})

	tests := []struct {
		name    string
		version string
		commit  string
		branch  string
		want    string
	}{
		{name: "master", version: "2189403", commit: "2189403", branch: "master", want: "master-2189403"},
		{name: "master ref", version: "2189403", commit: "2189403", branch: "refs/heads/master", want: "master-2189403"},
		{name: "release tag", version: "v1.2.3", commit: "2189403", branch: "release", want: "v1.2.3"},
		{name: "unknown commit", version: "dev", commit: "unknown", branch: "master", want: "dev"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Version, GitCommit, GitBranch = tt.version, tt.commit, tt.branch
			if got := Public(); got != tt.want {
				t.Fatalf("Public() = %q, want %q", got, tt.want)
			}
		})
	}
}
