package i18n

import "testing"

func TestSwedishNumbersUseCommaAndSpace(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"integer", Integer("sv", 1234567), "1 234 567"},
		{"negative integer", Integer("sv", -1234), "-1 234"},
		{"decimal", Decimal("sv", 1234.5, 2), "1 234,50"},
		{"English remains unchanged", Decimal("en", 1234.5, 2), "1234.50"},
		{"catalogue arguments", Sprintf("sv", "%d pussel · %.2f", 1892, 3.5), "1 892 pussel · 3,50"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}
