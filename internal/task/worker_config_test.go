package task

import "testing"

func TestParseWorkerConfig(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
		wantErr bool
	}{
		{"empty content defaults off", "", false, false},
		{"absent worker section defaults off", "[coordinator]\nbrief = \"x\"\n", false, false},
		{"isolate_network true", "[worker]\nisolate_network = true\n", true, false},
		{"isolate_network false", "[worker]\nisolate_network = false\n", false, false},
		{"quoted bool accepted", "[worker]\nisolate_network = \"on\"\n", true, false},
		{"comment ignored", "[worker]\n# turn it on\nisolate_network = true\n", true, false},
		{"malformed bool errors", "[worker]\nisolate_network = maybe\n", false, true},
		{"unknown key errors", "[worker]\nisolate_netwrok = true\n", false, true},
		{"malformed entry errors", "[worker]\nisolate_network\n", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseWorkerTOML(tc.content)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (cfg=%+v)", cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.IsolateNetwork != tc.want {
				t.Fatalf("IsolateNetwork = %v, want %v", cfg.IsolateNetwork, tc.want)
			}
		})
	}
}
