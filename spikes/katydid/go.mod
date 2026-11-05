module hotline-spike-katydid

go 1.26.3

require (
	github.com/onsi/ginkgo/v2 v2.34.0
	github.com/onsi/gomega v1.44.0
	go.katydid.org.za/parser-go v0.13.0
	go.katydid.org.za/parser-go-json v0.9.0
	go.katydid.org.za/validator-go-jsonschema v0.2.0
)

require (
	github.com/Masterminds/semver/v3 v3.4.0 // indirect
	github.com/dlclark/regexp2/v2 v2.0.1 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-task/slim-sprig/v3 v3.0.0 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/pprof v0.0.0-20260402051712-545e8a4df936 // indirect
	github.com/jtacoma/uritemplates v1.0.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
	go.katydid.org.za/validator-go v0.9.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/tools v0.51.0 // indirect
)

replace go.katydid.org.za/validator-go => codeberg.org/petercipov/validator-go v0.9.1-0.20261105130841-0bc6b0380f04

replace go.katydid.org.za/validator-go-jsonschema => codeberg.org/petercipov/validator-go-jsonschema v0.2.1-0.20261105130910-3f1c0cc8a511

replace github.com/santhosh-tekuri/jsonschema/v6 => github.com/petercipov/jsonschema/v6 v6.0.0-20261102215937-5cc57679620b

replace go.katydid.org.za/parser-go-json => codeberg.org/petercipov/parser-go-json v0.9.1-0.20261105082103-5b23f69c4fba
