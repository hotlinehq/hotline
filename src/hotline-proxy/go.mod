module hotline-proxy

go 1.27.3

require (
	github.com/cucumber/godog v0.16.0
	github.com/onsi/ginkgo/v2 v2.32.2
	github.com/onsi/gomega v1.43.0
	github.com/speakeasy-api/openapi v1.26.2
	go.katydid.org.za/parser-go v0.13.0
	go.katydid.org.za/parser-go-json v0.9.0
	go.katydid.org.za/validator-go-jsonschema v0.2.0
	go.opentelemetry.io/contrib/instrumentation/runtime v0.72.0
	go.opentelemetry.io/otel v1.47.0
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc v0.23.0
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp v0.23.0
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.47.0
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.47.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.47.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.47.0
	go.opentelemetry.io/otel/log v1.47.0
	go.opentelemetry.io/otel/metric v1.47.0
	go.opentelemetry.io/otel/sdk v1.47.0
	go.opentelemetry.io/otel/sdk/log v1.47.0
	go.opentelemetry.io/otel/sdk/metric v1.47.0
	go.opentelemetry.io/otel/trace v1.47.0
	go.opentelemetry.io/proto/otlp v1.11.1
	go.uber.org/goleak v1.3.0
	go.yaml.in/yaml/v3 v3.0.5
	golang.org/x/sync v0.24.0
	google.golang.org/grpc v1.85.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/Masterminds/semver/v3 v3.5.0 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cucumber/gherkin/go/v42 v42.0.0 // indirect
	github.com/cucumber/messages/go/v34 v34.2.0 // indirect
	github.com/dlclark/regexp2/v2 v2.0.1 // indirect
	github.com/dprotaso/go-yit v0.0.0-20250804204205-7afe4252a1dd // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-task/slim-sprig/v3 v3.0.0 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/pprof v0.0.0-20260906184651-6331bc6350fe // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.31.0 // indirect
	github.com/hashicorp/go-immutable-radix v1.3.1 // indirect
	github.com/hashicorp/go-memdb v1.3.5 // indirect
	github.com/hashicorp/golang-lru v0.5.4 // indirect
	github.com/jtacoma/uritemplates v1.0.0 // indirect
	github.com/speakeasy-api/jsonpath v0.7.0 // indirect
	github.com/speakeasy-api/yaml-jsonpath v0.0.0-20261016002013-07e5f48e6d17 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	go.katydid.org.za/validator-go v0.9.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.47.0 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/tools v0.51.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260928230214-8a89bd6388cc // indirect
)

replace go.katydid.org.za/validator-go => codeberg.org/petercipov/validator-go v0.9.1-0.20261105130841-0bc6b0380f04

replace go.katydid.org.za/validator-go-jsonschema => codeberg.org/petercipov/validator-go-jsonschema v0.2.1-0.20261105134111-98b0d70dcefe

replace go.katydid.org.za/parser-go-json => codeberg.org/petercipov/parser-go-json v0.9.1-0.20261105082103-5b23f69c4fba
