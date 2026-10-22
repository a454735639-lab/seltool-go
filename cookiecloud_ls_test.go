package main

import "testing"

func TestParseLocalStorageData(t *testing.T) {
	payload := map[string]any{
		"local_storage_data": map[string]any{
			"rousi.pro": map[string]any{
				"token": "pgk_test123",
				"theme": "dark",
			},
			"arr.example.com": []any{
				map[string]any{"name": "k1", "value": "v1"},
				map[string]any{"key": "k2", "value": float64(42)},
			},
			"empty.example": map[string]any{},
		},
	}
	got, err := parseLocalStorageData(payload)
	if err != nil {
		t.Fatalf("parseLocalStorageData error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("domains = %d, want 2 (empty domain should be skipped)", len(got))
	}
	rousi := got["rousi.pro"]
	if len(rousi) != 2 {
		t.Fatalf("rousi.pro items = %d, want 2", len(rousi))
	}
	if rousi[0].Key != "theme" || rousi[0].Value != "dark" {
		t.Errorf("rousi[0] = %+v, want theme=dark", rousi[0])
	}
	if rousi[1].Key != "token" || rousi[1].Value != "pgk_test123" {
		t.Errorf("rousi[1] = %+v, want token=pgk_test123", rousi[1])
	}
	arr := got["arr.example.com"]
	if len(arr) != 2 {
		t.Fatalf("arr items = %d, want 2", len(arr))
	}
	if arr[1].Key != "k2" || arr[1].Value != "42" {
		t.Errorf("arr[1] = %+v, want k2=42", arr[1])
	}
}

func TestParseLocalStorageDataMissing(t *testing.T) {
	if got, err := parseLocalStorageData(map[string]any{}); err != nil || got != nil {
		t.Fatalf("missing field: got=%v err=%v, want nil/nil", got, err)
	}
	if got, err := parseLocalStorageData(map[string]any{"local_storage_data": nil}); err != nil || got != nil {
		t.Fatalf("nil field: got=%v err=%v, want nil/nil", got, err)
	}
}

func TestLocalStorageForMatchesDomain(t *testing.T) {
	data := map[string][]LocalStorage{
		"rousi.pro":       {{Key: "token", Value: "abc"}},
		"www.example.com": {{Key: "k", Value: "v"}},
	}
	c := &CookieCloud{}
	if got := c.LocalStorageFor(data, Target{URL: "https://rousi.pro/"}); len(got) != 1 || got[0].Key != "token" {
		t.Fatalf("rousi match = %+v, want token", got)
	}
	if got := c.LocalStorageFor(data, Target{URL: "https://www.example.com/x"}); len(got) != 1 || got[0].Key != "k" {
		t.Fatalf("www match = %+v, want k", got)
	}
	if got := c.LocalStorageFor(data, Target{URL: "https://other.com/"}); len(got) != 0 {
		t.Fatalf("other match = %+v, want empty", got)
	}
}
