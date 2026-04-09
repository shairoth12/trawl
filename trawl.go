// Package trawl provides static analysis for detecting external service calls
// reachable from a given entry point function.
package trawl

import (
	"strings"
)

// ServiceType identifies the category of an external service matched by an indicator.
// User-defined service types can be expressed as ServiceType("CUSTOM").
type ServiceType string

// Built-in service type constants.
const (
	ServiceTypeHTTP          ServiceType = "HTTP"
	ServiceTypeGRPC          ServiceType = "GRPC"
	ServiceTypeRedis         ServiceType = "REDIS"
	ServiceTypePubSub        ServiceType = "PUBSUB"
	ServiceTypeDatastore     ServiceType = "DATASTORE"
	ServiceTypeFirestore     ServiceType = "FIRESTORE"
	ServiceTypePostgres      ServiceType = "POSTGRES"
	ServiceTypeElasticsearch ServiceType = "ELASTICSEARCH"
	ServiceTypeVault         ServiceType = "VAULT"
	ServiceTypeEtcd          ServiceType = "ETCD"
)

// AnalysisStats holds diagnostic measurements from a single analysis run.
// All duration fields are wall-clock milliseconds measured during the run.
// Stats is only populated when the --stats flag is provided.
type AnalysisStats struct {
	PackagesLoaded int   `json:"packages_loaded"`  // total packages loaded transitively
	CallGraphNodes int   `json:"call_graph_nodes"` // total functions in the call graph
	CallGraphEdges int   `json:"call_graph_edges"` // total call sites in the call graph
	NodesVisited   int   `json:"nodes_visited"`    // unique functions entered during DFS
	EdgesExamined  int   `json:"edges_examined"`   // total edges considered during DFS (including skipped)
	LoadDurationMs int64 `json:"load_duration_ms"` // milliseconds spent loading packages
	WalkDurationMs int64 `json:"walk_duration_ms"` // milliseconds spent walking the call graph
}

// Result holds the analysis output for a single entry point function.
type Result struct {
	EntryPoint    string         `json:"entry_point"`
	Package       string         `json:"package"`
	ExternalCalls []ExternalCall `json:"external_calls"`
	Deduplicated  bool           `json:"deduplicated,omitempty"`
	Stats         *AnalysisStats `json:"stats,omitempty"`
}

// NewResult returns a Result with ExternalCalls initialized to a non-nil empty slice.
func NewResult(entryPoint, pkg string) Result {
	return Result{
		EntryPoint:    entryPoint,
		Package:       pkg,
		ExternalCalls: []ExternalCall{},
	}
}

// ExternalCall describes a single detected call to an external service reachable from the entry point.
type ExternalCall struct {
	ServiceType    ServiceType `json:"service_type"`     // matched service label, e.g. ServiceTypeRedis
	ImportPath     string      `json:"import_path"`      // Go import path of the called package
	Function       string      `json:"function"`         // fully-qualified SSA function name
	File           string      `json:"file"`             // source file containing the call site
	Line           int         `json:"line"`             // line number of the call site
	CallChain      []string    `json:"call_chain"`       // ordered function names from entry point to call site; never nil in valid results
	ResolvedVia    string      `json:"resolved_via"`     // how the call was discovered: direct, mock_inference, cross_module_inference
	Confidence     string      `json:"confidence"`       // reliability of the detection: high, medium, low
	ShortFunction  string      `json:"short_function"`   // Function with module paths and generic type params stripped
	ShortCallChain []string    `json:"short_call_chain"` // CallChain with module paths and generic type params stripped
}

// ResolvedVia values describe how an external call was discovered.
const (
	ResolvedViaDirect               = "direct"
	ResolvedViaMockInference        = "mock_inference"
	ResolvedViaCrossModuleInference = "cross_module_inference"
)

// Confidence values indicate the reliability of a detection.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// ShortenName strips module path prefixes and generic type parameters from
// a fully-qualified Go SSA function name, producing a concise form suitable
// for LLM consumption.
//
// Examples:
//
//	"github.com/foo/bar.Get"                          → "Get"
//	"(*github.com/foo/bar.Client).Do"                 → "(*Client).Do"
//	"github.com/foo/bar.Cache[github.com/a/b.T].Set"  → "Cache.Set"
func ShortenName(s string) string {
	s = stripGenericParams(s)

	lastSlash := strings.LastIndex(s, "/")
	if lastSlash == -1 {
		return s
	}

	dotAfterSlash := strings.IndexByte(s[lastSlash:], '.')
	if dotAfterSlash == -1 {
		return s
	}

	// Preserve prefix characters before the package path, e.g. "(*".
	pathStart := 0
	for pathStart < lastSlash && (s[pathStart] == '(' || s[pathStart] == '*') {
		pathStart++
	}

	prefix := s[:pathStart]
	suffix := s[lastSlash+dotAfterSlash+1:]
	return prefix + suffix
}

// stripGenericParams removes Go generic type parameter blocks ([...]) from s,
// handling nested brackets. Iterative to handle strings with multiple
// top-level bracket pairs without additional stack frames.
func stripGenericParams(s string) string {
	for {
		start := strings.IndexByte(s, '[')
		if start == -1 {
			return s
		}
		depth := 0
		for i := start; i < len(s); i++ {
			switch s[i] {
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					s = s[:start] + s[i+1:]
					goto next
				}
			}
		}
		return s // unmatched '[', bail
	next:
	}
}

// Indicator maps an import path prefix to a named service type for detection purposes.
// When SkipInternal is true, subpackages under /internal/ within the indicator prefix
// are excluded from matching, preventing false positives from library internals.
// WrapperFor lists additional import path prefixes that should be classified under
// the same ServiceType. This allows declaring wrapper libraries explicitly so that
// calls through them receive a direct, high-confidence classification.
type Indicator struct {
	Package      string      `yaml:"package"      json:"package"`
	ServiceType  ServiceType `yaml:"service_type" json:"service_type"`
	SkipInternal bool        `yaml:"skip_internal,omitempty" json:"skip_internal,omitempty"`
	WrapperFor   []string    `yaml:"wrapper_for,omitempty"   json:"wrapper_for,omitempty"`
}

// Config holds user-supplied analysis configuration loaded from a YAML file.
type Config struct {
	Indicators []Indicator `yaml:"indicators"`
}
