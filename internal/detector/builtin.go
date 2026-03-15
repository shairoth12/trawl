// Package detector matches package import paths against service type indicators
// to classify external calls.
package detector

import "github.com/shairoth12/trawl"

var builtinIndicators = []trawl.Indicator{
	{Package: "github.com/go-redis/redis", ServiceType: trawl.ServiceTypeRedis, SkipInternal: true},
	{Package: "github.com/redis/go-redis", ServiceType: trawl.ServiceTypeRedis, SkipInternal: true},
	{Package: "google.golang.org/grpc", ServiceType: trawl.ServiceTypeGRPC, SkipInternal: true},
	{Package: "net/http", ServiceType: trawl.ServiceTypeHTTP, SkipInternal: true},
	{Package: "cloud.google.com/go/pubsub", ServiceType: trawl.ServiceTypePubSub, SkipInternal: true},
	{Package: "cloud.google.com/go/datastore", ServiceType: trawl.ServiceTypeDatastore, SkipInternal: true},
	{Package: "cloud.google.com/go/firestore", ServiceType: trawl.ServiceTypeFirestore, SkipInternal: true},
	{Package: "database/sql", ServiceType: trawl.ServiceTypePostgres, SkipInternal: true},
	{Package: "github.com/lib/pq", ServiceType: trawl.ServiceTypePostgres, SkipInternal: true},
	{Package: "github.com/jackc/pgx", ServiceType: trawl.ServiceTypePostgres, SkipInternal: true},
	{Package: "github.com/elastic/go-elasticsearch", ServiceType: trawl.ServiceTypeElasticsearch, SkipInternal: true},
	{Package: "github.com/hashicorp/vault/api", ServiceType: trawl.ServiceTypeVault, SkipInternal: true},
	{Package: "go.etcd.io/etcd/client", ServiceType: trawl.ServiceTypeEtcd, SkipInternal: true},
}
