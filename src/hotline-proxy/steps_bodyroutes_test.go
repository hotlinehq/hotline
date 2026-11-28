package proxy_test

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cucumber/godog"
)

const snsHost = "sns.eu-west-1.amazonaws.com"

// snsLikeSpec names each action in the body field Action, the way SNS and
// STS do.
const snsLikeSpec = `openapi: 3.2.0
info: { title: SNS, version: '1' }
x-hotline-action-field: Action
paths:
  /#Action=Publish:
    post:
      requestBody:
        content:
          application/x-www-form-urlencoded:
            schema: { $ref: '#/components/schemas/Publish' }
          application/json:
            schema: { $ref: '#/components/schemas/Publish' }
      responses:
        '200':
          description: done
          content:
            text/xml:
              schema:
                type: object
                xml: { name: PublishResponse }
                required: [PublishResult]
                properties:
                  PublishResult:
                    type: object
                    required: [MessageId]
                    properties:
                      MessageId: { type: string }
  /#Action=ListTopics:
    post:
      requestBody:
        content:
          application/x-www-form-urlencoded:
            schema: { type: object, properties: { Action: { type: string } } }
      responses:
        '200': { description: done }
components:
  schemas:
    Publish:
      type: object
      required: [Action, TopicArn, Message]
      properties:
        Action: { type: string }
        TopicArn: { type: string }
        Message: { type: string }
`

func bodyRouteSteps(sc *godog.ScenarioContext, sut *specChecksSUT) {
	sc.Step(`^the SNS-like spec$`, func() error {
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.specAPI(snsHost, snsLikeSpec, "")))
		return nil
	})
	sc.Step(`^the app sends the (form|json|text) body (.*)$`, func(kind, body string) error {
		return sut.snsCall(kind, body)
	})
	sc.Step(`^the app sends a form body of (\d+) MiB that starts with Action=Publish$`, func(mib int) error {
		body := "Action=Publish&TopicArn=order-paid&Message=" + strings.Repeat("a", mib<<20)
		return sut.snsCall("form", body)
	})
	sc.Step(`^SNS answers 200 with the xml body (.*)$`, func(body string) error {
		sut.vendors.vendor(snsHost).setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Content-Type", "text/xml")
			_, _ = rw.Write([]byte(body))
		})
		return nil
	})
	sc.Step(`^SNS got the body (.*)$`, func(body string) error {
		calls := sut.vendors.vendor(snsHost).received()
		if len(calls) == 0 {
			return failf("SNS got no call")
		}
		if got := string(calls[len(calls)-1].body); got != body {
			return failf("SNS got the body %q", got)
		}
		return nil
	})
	sc.Step(`^the SNS api with a spec whose route is (\S+) and (no action mark|.+)$`, func(route, marks string) error {
		lines := ""
		if marks != "no action mark" {
			lines = strings.Join(strings.Split(marks, " and "), "\n") + "\n"
		}
		spec := "openapi: 3.2.0\ninfo: { title: SNS, version: '1' }\n" + lines +
			"paths:\n  " + route + ":\n    post:\n      responses:\n        '200': { description: done }\n"
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.specAPI(snsHost, spec, "")))
		return nil
	})
	sc.Step(`^the app sends SendMessage to the invoices queue with a message of (\d+) MiB$`, func(mib int) error {
		body, err := json.Marshal(map[string]string{"QueueUrl": invoicesQueue, "MessageBody": strings.Repeat("a", mib<<20)})
		if err != nil {
			return err
		}
		return sut.aws.sqsCall(sut.app.appID, []string{"X-Amz-Target: AmazonSQS.SendMessage"}, string(body))
	})
}

// snsCall sends one call to the fake SNS, the way the AWS SDK does: POST /
// with the action in the body.
func (sut *specChecksSUT) snsCall(kind, body string) error {
	contentType := map[string]string{
		"form": "application/x-www-form-urlencoded",
		"json": "application/json",
		"text": "text/plain",
	}[kind]
	lines := []string{"Content-Type: " + contentType}
	if sut.app.appID != "" {
		lines = append([]string{basic(sut.app.appID + ":x")}, lines...)
	}
	return sut.app.send(request{
		method:  http.MethodPost,
		target:  "http://" + snsHost + "/",
		headers: lines,
		body:    strings.NewReader(body),
		length:  len(body),
	})
}
