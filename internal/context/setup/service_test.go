package contextinit

import (
	"context"
	"reflect"
	"testing"
)

func TestOrderedSortsByOrderThenNameAndSkipsNilServices(t *testing.T) {
	var calls []string
	service := func(name string) Service {
		return ServiceFunc(func(context.Context, Params) error {
			calls = append(calls, name)
			return nil
		})
	}
	registrations := []Registration{
		{Name: "b", Order: 20, Service: service("b")},
		{Name: "nil", Order: 5},
		{Name: "first", Order: 10, Service: service("first")},
		{Name: "a", Order: 20, Service: service("a")},
	}
	before := append([]Registration(nil), registrations...)

	services := Ordered(registrations)
	if len(services) != 3 {
		t.Fatalf("services = %#v", services)
	}
	for _, svc := range services {
		if err := svc.InitContext(context.Background(), Params{}); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"first", "a", "b"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	for i := range registrations {
		if registrations[i].Name != before[i].Name || registrations[i].Order != before[i].Order {
			t.Fatalf("registrations mutated: %#v", registrations)
		}
	}
}
