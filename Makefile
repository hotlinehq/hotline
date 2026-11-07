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
# each, one folder load/results/run-NNN per line below. The proxy is built
# with -tags loadprofile so the lines marked -profile can also save a
# processor and a memory profile. See load/README.md.
.PHONY: load
load:
	mkdir -p load/out
	cd src/hotline-proxy && go build -tags loadprofile -o ../../load/out/hotline-proxy ./cmd
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -shape small-json -rate 500
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -shape small-json -rate 1000
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -shape small-json -rate 2000
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -shape small-json -rate 5000
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -shape small-json -rate 10000
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -shape small-json -rate 20000
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -shape small-json -rate 5000 -duration 120s -label long
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -shape medium-json -rate 400
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -shape large-json -rate 200 -profile
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -shape large-json -rate 400
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -shape large-json -rate 500
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -shape huge-json -rate 50 -profile
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/s3/2006-03-01.openapi.yaml -shape xml -rate 500 -profile
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -more-specs specs/aws -label all-specs -shape large-json -rate 200
	go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -more-specs specs/aws -label all-specs -shape huge-json -rate 50

# load-compare measures two builds in turn, so a fix can be judged against
# main on the same computer in the same job: BASE_PROXY is the built
# hotline-proxy of the other build (default label main; this checkout is
# labelled branch). Each shape and rate runs one repeat of main, one of the
# branch, three times over; nothing is averaged. The table at the end sets
# every repeat side by side and gives the range of change. Each line of
# LOAD_RUNS is a shape, a rate and the spec its calls are checked against.
BASE_LABEL ?= main
CAND_LABEL ?= branch
SSM_SPEC = specs/aws/ssm/2014-11-06.openapi.yaml
S3_SPEC = specs/aws/s3/2006-03-01.openapi.yaml
LOAD_RUNS = "small-json 1000 $(SSM_SPEC)" "medium-json 400 $(SSM_SPEC)" "large-json 200 $(SSM_SPEC)" \
	"large-json 500 $(SSM_SPEC)" "huge-json 50 $(SSM_SPEC)" "xml 500 $(S3_SPEC)"
.PHONY: load-compare
load-compare:
	test -n "$(BASE_PROXY)" || { echo "set BASE_PROXY to the built hotline-proxy of the build to compare with"; exit 1; }
	mkdir -p load/out
	cd src/hotline-proxy && go build -o ../../load/out/hotline-proxy ./cmd
	go build -o load/out/load ./load/cmd/load
	for line in $(LOAD_RUNS); do \
		set -- $$line; \
		for i in 1 2 3; do \
			load/out/load -proxy $(BASE_PROXY) -label $(BASE_LABEL) -spec $$3 -shape $$1 -rate $$2 -repeats 1 || exit 1; \
			load/out/load -proxy load/out/hotline-proxy -label $(CAND_LABEL) -spec $$3 -shape $$1 -rate $$2 -repeats 1 || exit 1; \
		done; \
	done
	go run ./load/cmd/compare load/results | tee load/results/compare.txt

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
