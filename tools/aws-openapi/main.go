// Command aws-openapi writes the OpenAPI spec of an AWS service from its
// Smithy model, for the specs folder. It knows AWS's JSON protocol
// (SQS, DynamoDB and the others listed in specs/aws), its REST and XML
// one (S3), its query one (SNS, STS: the action in the form field Action)
// and its REST and JSON one (Lambda, SES v2):
//
//	go run . -record QueueUrl=messaging.destination.name -record-action messaging.operation.name models/sqs-2012-11-05.json > ../../specs/aws/sqs/2012-11-05.openapi.yaml
//	go run . models/s3-2006-03-01.json > ../../specs/aws/s3/2006-03-01.openapi.yaml
//	go run . -record TopicArn=messaging.destination.name -record-action messaging.operation.name models/sns-2010-03-31.json > ../../specs/aws/sns/2010-03-31.openapi.yaml
//	go run . -record-action rpc.method models/sts-2011-06-15.json > ../../specs/aws/sts/2011-06-15.openapi.yaml
//	go run . models/lambda-2015-03-31.json > ../../specs/aws/lambda/2015-03-31.openapi.yaml
//
// Each -record FIELD=NAME marks that field of every action's input, so the
// proxy copies it into records as NAME. -record-action NAME lists the
// X-Amz-Target header on every action, or marks the Action form field,
// copied into records as NAME.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"hotline-tools/aws-openapi/smithy"
)

var errRecord = errors.New("not FIELD=NAME")

func main() {
	record := map[string]string{}
	flag.Func("record", "FIELD=NAME: copy FIELD of every action's input into records as NAME", func(v string) error {
		field, name, ok := strings.Cut(v, "=")
		if !ok || field == "" || name == "" {
			return fmt.Errorf("%w: %q", errRecord, v)
		}
		record[field] = name
		return nil
	})
	action := flag.String("record-action", "", "NAME: copy the X-Amz-Target header into records as NAME")
	flag.Parse()
	if flag.NArg() != 1 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: aws-openapi [-record FIELD=NAME]... [-record-action NAME] MODEL.json > SPEC.openapi.yaml")
		os.Exit(2)
	}
	data, err := os.ReadFile(flag.Arg(0))
	if err == nil {
		data, err = smithy.Convert(data, smithy.Options{Record: record, Action: *action})
	}
	if err == nil {
		_, err = os.Stdout.Write(data)
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
