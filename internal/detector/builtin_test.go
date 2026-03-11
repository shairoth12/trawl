package detector

import (
	"testing"

	"github.com/shairoth12/trawl"
)

func TestBuiltinIndicators_Count(t *testing.T) {
	want := 13
	got := len(builtinIndicators)
	if got != want {
		t.Errorf("builtinIndicators: got %d entries, want %d", got, want)
	}
}

func TestBuiltinIndicators_NoEmptyFields(t *testing.T) {
	for i, ind := range builtinIndicators {
		if ind.Package == "" {
			t.Errorf("builtinIndicators[%d]: Package is empty", i)
		}
		if ind.ServiceType == "" {
			t.Errorf("builtinIndicators[%d]: ServiceType is empty", i)
		}
	}
}

func TestBuiltinIndicators_ServiceTypes(t *testing.T) {
	required := []trawl.ServiceType{
		trawl.ServiceTypeRedis,
		trawl.ServiceTypeGRPC,
		trawl.ServiceTypeHTTP,
		trawl.ServiceTypePubSub,
		trawl.ServiceTypeDatastore,
		trawl.ServiceTypeFirestore,
		trawl.ServiceTypePostgres,
		trawl.ServiceTypeElasticsearch,
		trawl.ServiceTypeVault,
		trawl.ServiceTypeEtcd,
	}

	found := make(map[trawl.ServiceType]bool, len(builtinIndicators))
	for _, ind := range builtinIndicators {
		found[ind.ServiceType] = true
	}

	for _, st := range required {
		if !found[st] {
			t.Errorf("builtinIndicators: missing service type %q", st)
		}
	}
}
