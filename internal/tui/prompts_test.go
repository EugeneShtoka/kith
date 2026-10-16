package tui

import "testing"

// Every prompt kind has a spec that labels it and acts on enter.
func TestEveryPromptKindHasASpec(t *testing.T) {
	t.Parallel()
	for kind := promptNone + 1; kind < promptKinds; kind++ {
		spec, ok := promptSpecs[kind]
		if !ok {
			t.Errorf("prompt kind %d has no spec", kind)
			continue
		}
		if spec.label == "" && spec.labelOf == nil {
			t.Errorf("prompt kind %d has no label", kind)
		}
		if spec.submit == nil {
			t.Errorf("prompt kind %d does nothing on enter", kind)
		}
	}
}

// Every picker kind acts on enter: a list on its chosen row, a set on its ticked rows.
func TestEveryPickerKindActsOnEnter(t *testing.T) {
	t.Parallel()
	for kind, spec := range pickerSpecs {
		act := pickerActions[kind]
		if spec.multi && act.apply == nil {
			t.Errorf("picker kind %d is a set that applies nothing", kind)
		}
		if !spec.multi && act.choose == nil {
			t.Errorf("picker kind %d chooses nothing", kind)
		}
	}
}
