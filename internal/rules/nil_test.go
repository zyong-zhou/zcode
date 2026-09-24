package rules

import "testing"

func TestNilStoreAccessors(t *testing.T) {
	var s *Store
	if got := s.Render(); got != "" {
		t.Errorf("nil Render = %q, want empty", got)
	}
	if got := s.Files(); got != nil {
		t.Errorf("nil Files = %v, want nil", got)
	}
}
