// Package trawl provides static analysis for detecting external service calls
// reachable from a given entry point function.
package trawl

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

// Result holds the analysis output for a single entry point function.
type Result struct {
	EntryPoint    string         `json:"entry_point"`
	Package       string         `json:"package"`
	ExternalCalls []ExternalCall `json:"external_calls"`
	Deduplicated  bool           `json:"deduplicated,omitempty"`
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
	ServiceType ServiceType `json:"service_type"` // matched service label, e.g. ServiceTypeRedis
	ImportPath  string      `json:"import_path"`  // Go import path of the called package
	Function    string      `json:"function"`
	File        string      `json:"file"`
	Line        int         `json:"line"`
	CallChain   []string    `json:"call_chain"`   // ordered function names from entry point to call site; never nil in valid results
	ResolvedVia string      `json:"resolved_via"` // how the call was discovered: direct, mock_inference, cross_module_inference
	Confidence  string      `json:"confidence"`   // reliability of the detection: high, medium, low
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
