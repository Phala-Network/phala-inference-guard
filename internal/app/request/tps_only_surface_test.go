package request

import (
	"reflect"
	"testing"
)

func TestTPSOnlyClassifierDoesNotOwnRetiredResourceEstimation(t *testing.T) {
	tests := []struct {
		name    string
		typeOf  reflect.Type
		retired []string
	}{
		{
			name:    "Config",
			typeOf:  reflect.TypeOf(Config{}),
			retired: []string{"OutputTokenFields", "Estimator"},
		},
		{
			name:    "Classification",
			typeOf:  reflect.TypeOf(Classification{}),
			retired: []string{"Cost"},
		},
	}
	for _, test := range tests {
		for _, field := range test.retired {
			if _, exists := test.typeOf.FieldByName(field); exists {
				t.Errorf("%s retained TPS-external field %s", test.name, field)
			}
		}
	}
}
