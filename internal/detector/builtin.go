// Package detector matches package import paths against service type indicators
// to classify external calls.
package detector

import "github.com/shairoth12/trawl"

var builtinIndicators = []trawl.Indicator{
	{Package: "github.com/go-redis/redis", ServiceType: trawl.ServiceTypeRedis},
	{Package: "github.com/redis/go-redis", ServiceType: trawl.ServiceTypeRedis},
	{Package: "google.golang.org/grpc", ServiceType: trawl.ServiceTypeGRPC},
	{Package: "net/http", ServiceType: trawl.ServiceTypeHTTP},
	{Package: "cloud.google.com/go/pubsub", ServiceType: trawl.ServiceTypePubSub},
	{Package: "cloud.google.com/go/datastore", ServiceType: trawl.ServiceTypeDatastore},
	{Package: "cloud.google.com/go/firestore", ServiceType: trawl.ServiceTypeFirestore},
	{Package: "database/sql", ServiceType: trawl.ServiceTypePostgres},
	{Package: "github.com/lib/pq", ServiceType: trawl.ServiceTypePostgres},
	{Package: "github.com/jackc/pgx", ServiceType: trawl.ServiceTypePostgres},
	{Package: "github.com/elastic/go-elasticsearch", ServiceType: trawl.ServiceTypeElasticsearch},
	{Package: "github.com/hashicorp/vault/api", ServiceType: trawl.ServiceTypeVault},
	{Package: "go.etcd.io/etcd/client", ServiceType: trawl.ServiceTypeEtcd},
}
