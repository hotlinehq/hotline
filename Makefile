.SILENT:

gen_collector:
	~/go/bin/ocb --verbose --config ./src/otelcol-dev/builder-config.yaml

test:
	rm -f cover.out
	go clean -testcache
	go test  ./src/hotline/... ./src/hotline-proxy/... -coverprofile=./cover.out -covermode=atomic -coverpkg=./src/hotline/...,./src/hotline-proxy/...
	go test  ./src/otel-hotline/...
	go test  ./examples/aws-shop/...
	go test  ./tools/aws-openapi/...
	go test  ./load/...
	$(MAKE) e2e
	tail -n +2 e2e-cover.out >> cover.out

# e2e runs the @e2e scenarios against the built hotline-proxy program. The
# program is built to record coverage, which lands in e2e-cover.out.
e2e:
	rm -rf e2e-cover e2e-cover.out
	mkdir -p e2e-cover/data
	cd src/hotline-proxy && go build -cover -covermode=atomic -coverpkg=./... -o ../../e2e-cover/hotline-proxy ./cmd
	HOTLINE_PROXY=$(CURDIR)/e2e-cover/hotline-proxy GOCOVERDIR=$(CURDIR)/e2e-cover/data \
		go test -tags e2e -count=1 -run TestEndToEnd ./src/hotline-proxy/
	go tool covdata textfmt -i=e2e-cover/data -o e2e-cover.out

# load builds the proxy and runs the load test: the fakes, the proxy and k6
# (which must be installed), each call shape at its rates, three repeats
# each, one folder load/results/run-NNN per line below. See load/README.md.
load:
	mkdir -p load/out
	cd src/hotline-proxy && go build -o ../../load/out/hotline-proxy ./cmd
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -shape small-json -rate 500
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -shape small-json -rate 1000
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -shape small-json -rate 2000
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -shape large-json -rate 200

clean-cache:
	go clean -testcache
	go clean -cache

cover:
	test -f cover.out || { echo "cover.out not found, run 'make test' first"; exit 1; }
	go tool cover -func cover.out > cover.func.txt
	-grep -v "100.0" cover.func.txt
	! grep -q -v "100.0" cover.func.txt || exit 1

deps:
	go mod download

lint:
	golangci-lint run ./src/hotline/...
	golangci-lint run ./src/hotline-proxy/...
	golangci-lint run ./src/otel-hotline/...
	golangci-lint run ./examples/aws-shop/...
	golangci-lint run ./tools/aws-openapi/...
	golangci-lint run ./load/...
