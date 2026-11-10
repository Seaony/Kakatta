package analysis

import "testing"

func TestRiskNameKeyUsesExistingEvidenceRules(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
	}{
		{"width and punctuation", " ＡＢＣ，电池（鼓包）！？ ", "ABC电池鼓包"},
		{"spaces", "电 池\n鼓\t包", "电池鼓包"},
		{"case remains meaningful", "abc", "abc"},
		{"other symbols remain meaningful", "A/B-1", "A/B-1"},
		{"empty", " \t，。", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RiskNameKey(tc.input); got != tc.want || got != evidenceText(tc.input) {
				t.Fatalf("key=%q want %q", got, tc.want)
			}
		})
	}
}
