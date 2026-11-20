package config

import (
	"context"
	"testing"

	"github.com/kncept/Emissary/mailer/envutil"
)

func TestLoadMaxFileSize(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    int64
		wantErr bool
	}{
		{name: "default", value: "", want: DefaultMaxFileSize},
		{name: "custom", value: "2097152", want: 2 << 20},
		{name: "zero rejected", value: "0", wantErr: true},
		{name: "negative rejected", value: "-1", wantErr: true},
		{name: "non numeric rejected", value: "10MiB", wantErr: true},
		{name: "absurdly large rejected", value: "1099511627776", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			envutil.ClearCache()
			envutil.SetOverride("MAX_FILESIZE", tc.value)
			t.Cleanup(envutil.ClearCache)

			cfg, err := Load(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Load() succeeded, want error for MAX_FILESIZE=%q", tc.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.MaxFileSize != tc.want {
				t.Errorf("MaxFileSize = %d, want %d", cfg.MaxFileSize, tc.want)
			}
		})
	}
}
