package domain

import "testing"

func TestParseRoleAliases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  Role
		ok    bool
	}{
		{name: "top live value", input: "TOP", want: RoleTop, ok: true},
		{name: "jungle", input: "jungle", want: RoleJungle, ok: true},
		{name: "middle live value", input: "MIDDLE", want: RoleMiddle, ok: true},
		{name: "mid manual alias", input: "mid", want: RoleMiddle, ok: true},
		{name: "bottom live value", input: "BOTTOM", want: RoleBottom, ok: true},
		{name: "adc manual alias", input: "adc", want: RoleBottom, ok: true},
		{name: "bot manual alias", input: "bot", want: RoleBottom, ok: true},
		{name: "utility live value", input: "UTILITY", want: RoleSupport, ok: true},
		{name: "support manual value", input: "support", want: RoleSupport, ok: true},
		{name: "support short alias", input: "sup", want: RoleSupport, ok: true},
		{name: "missing stays unknown", input: "", ok: false},
		{name: "unknown stays unknown", input: "roamer", ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := ParseRole(test.input)
			if ok != test.ok || got != test.want {
				t.Fatalf("ParseRole(%q) = (%q, %t), want (%q, %t)", test.input, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestRoleValid(t *testing.T) {
	for _, role := range []Role{RoleTop, RoleJungle, RoleMiddle, RoleBottom, RoleSupport} {
		if !role.Valid() {
			t.Errorf("role %q should be valid", role)
		}
	}
	if Role("roamer").Valid() || Role("").Valid() {
		t.Fatal("unknown roles must remain invalid")
	}
}
