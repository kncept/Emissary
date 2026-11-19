package envutil

import "testing"

func TestStartupMessage(t *testing.T) {
	oldCommit, oldBuildTime := CommitHash, BuildTime
	t.Cleanup(func() {
		CommitHash, BuildTime = oldCommit, oldBuildTime
	})

	tests := []struct {
		name      string
		buildTime string
		want      string
	}{
		{
			name:      "ISO 8601 build time",
			buildTime: "2026-11-20T08:17:12Z",
			want:      "s3-receiver (b3edad7) built: 20/11/2026 08:17:12\n",
		},
		{
			name:      "already formatted build time",
			buildTime: "20/11/2026 08:17:12",
			want:      "s3-receiver (b3edad7) built: 20/11/2026 08:17:12\n",
		},
		{
			name:      "unset build time",
			buildTime: "unknown",
			want:      "s3-receiver (b3edad7) built: unknown\n",
		},
	}

	CommitHash = "b3edad7"
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			BuildTime = tt.buildTime
			if got := StartupMessage("s3-receiver"); got != tt.want {
				t.Errorf("StartupMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}
