package update

import "testing"

func TestValidateTag(t *testing.T) {
	valid := []string{"v1.2.3", "1.2.3", "v0.7.44", "v1.0.0-rc1"}
	for _, tag := range valid {
		if err := ValidateTag(tag); err != nil {
			t.Errorf("ValidateTag(%q) = %v, want nil", tag, err)
		}
	}
	invalid := []string{"", "latest; rm -rf /", "v1", "$(whoami)", "1.2.3 && curl evil"}
	for _, tag := range invalid {
		if err := ValidateTag(tag); err == nil {
			t.Errorf("ValidateTag(%q) = nil, want error (injection guard)", tag)
		}
	}
}
