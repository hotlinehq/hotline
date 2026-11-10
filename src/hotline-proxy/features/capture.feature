Feature: The capture ingress
  On a Mac, a small helper (a system extension, built in its own project,
  hotline-macos-capture) picks out the secure calls of the apps named in
  the --capture flag and hands them to the proxy's listen.intercept port.
  Each captured connection starts with one framed hello message that says
  where the program really called and which program it was, followed by the
  program's own bytes. The helper's control connection, on the same port,
  carries the app list from the proxy to the helper. From the hello on, the
  proxy treats a flow like any intercepted connection: it reads the site
  name from the TLS handshake, shows a certificate for it signed by the
  customer's issuer, and handles each call inside like any other call, with
  hotline.ingress capture (Peter, 2026-11-10).

  The port tells the two apart by the first byte: a TLS handshake starts
  with 0x16, the helper's framed message with 0x00. Without the app list
  (the proxy was not asked to capture), the helper's messages are refused
  like any other bytes that are not TLS.

  The tests need no Mac: the test extension is a program that connects to
  the port and sends the same messages.

  Background:
    Given an issuer named customer in issuers
    And listen.intercept picks it with integration "ci-lint"
    And the proxy captures the apps curl and firefox
    And an app that trusts the interception issuer

  Rule: Captured calls are handled like any other call

    Scenario: A call reaches the vendor unchanged, and the record names the program
      When the extension opens a flow to api.talon.one:443 for process 4242 at "/usr/bin/curl" and the app names api.talon.one in the handshake
      And the app sends its call
      Then the test vendor got the call over https with its Host, path, query, headers and body unchanged
      And the record says hotline.ingress capture and ID ci-lint
      And the record says the program is process 4242 at "/usr/bin/curl"

    Scenario: The certificate names the site in the handshake
      When the extension opens a flow to api.stripe.com:443 for process 4242 at "/usr/bin/curl" and the app names api.stripe.com in the handshake
      Then the app sees a certificate for api.stripe.com signed by the interception issuer

    Scenario: The port in the hello decides where the proxy connects
      When the extension opens a flow to api.talon.one:8443 for process 4242 at "/usr/bin/curl" and the app names api.talon.one in the handshake
      And the app sends its call with the header "Host: api.talon.one:8443"
      Then the test vendor got the call over https on port 8443

    Scenario: Without an integration the ID is not-passed
      Given listen.intercept picks it
      When the extension opens a flow to example.org:443 for process 4242 at "/usr/bin/curl" and the app names example.org in the handshake
      And the app sends its call with the header "Host: example.org"
      Then the record says hotline.ingress capture and ID not-passed

    Scenario: No extension is on the control connection
      The calls still work: the proxy only tells the extension what to pick.
      When the extension opens a flow to api.talon.one:443 for process 4242 at "/usr/bin/curl" and the app names api.talon.one in the handshake
      And the app sends its call
      Then the test vendor for api.talon.one got the call

  Rule: A handshake with no site name uses the address in the hello

    Scenario: A call by IP address is intercepted with the IP as host
      When the extension opens a flow to the address of api.talon.one:443 for process 4242 at "/usr/bin/curl" and the app names no site in the handshake
      Then the app sees a certificate for the IP api.talon.one signed by the interception issuer
      When the app sends its call
      Then the proxy connected to the address of api.talon.one
      And the record says hotline.ingress capture and ID ci-lint

  Rule: Hosts in passthrough are relayed untouched

    Scenario: A listed host is relayed by its name
      Given listen.intercept lists pinned.example.com in passthrough
      When the extension opens a flow to pinned.example.com:443 for process 4242 at "/usr/bin/curl" and the app names pinned.example.com in the handshake
      Then the proxy dialed pinned.example.com by its name, never the address the app chose
      And the vendor's own certificate reaches the app, not one from the interception issuer
      When the app closes its connections
      Then the capture record has hotline.ingress capture, server.address pinned.example.com, hotline.intercept.mode passthrough, hotline.intercept.reason passthrough and process.pid 4242

  Rule: Connections that cannot be intercepted are recorded with the program

    Scenario: An app that does not trust the issuer
      Given an app that does not trust the interception issuer
      When the extension opens a flow to api.talon.one:443 for process 4242 at "/usr/bin/curl" and the app names api.talon.one in the handshake
      Then TLS inside the tunnel fails to set up
      And nothing is sent to any vendor
      And the capture record has hotline.integration.id ci-lint, server.address api.talon.one, error.type tls_untrusted_by_app, process.pid 4242 and process.executable.path /usr/bin/curl

    Scenario: Plain text after a good hello
      When the extension opens a flow to api.talon.one:443 for process 4242 at "/usr/bin/curl" and sends plain HTTP bytes
      Then the capture record has hotline.ingress capture and error.type not_tls

  Rule: The app list goes to the extension on the control connection

    Scenario: The extension gets the list, with a skip rule for Hotline itself
      When the extension opens the control connection
      Then the extension receives the app list curl, firefox and a skip rule for Hotline's own process number

  Rule: Messages from the extension are not trusted

    Scenario Outline: A first message that is not a usable hello
      When the extension sends <message> as its first message
      Then the capture record has hotline.ingress capture and error.type capture_bad_hello

      Examples:
        | message                                |
        | a size over 16 KiB                     |
        | a body that is not JSON                |
        | a message cut short                    |
        | a hello with an unknown version        |
        | a flow with no host                    |
        | a flow with port 0                     |
        | a flow that is not TCP                 |

  Rule: Capturing needs the proxy to be asked, and a port only this Mac can reach

    Scenario: Without the app list the helper's hello is refused like any bytes that are not TLS
      Given the proxy captures no apps
      When the extension opens a flow to api.talon.one:443 for process 4242 at "/usr/bin/curl" and sends plain HTTP bytes
      Then the intercept record has hotline.ingress intercept and error.type not_tls

    Scenario: Without the app list the control connection gets no list
      Given the proxy captures no apps
      When the extension opens the control connection
      Then the extension's control connection is closed

    Scenario Outline: A proxy asked to capture that cannot do it does not start
      Given <problem>
      When the proxy loads the config
      Then the config is refused with a message naming the problem

      Examples:
        | problem                                                              |
        | the proxy is asked to capture but the config has no listen.intercept |
        | the proxy is asked to capture while listen.intercept listens on all interfaces |
        | the proxy is asked to capture an app that is just a skip mark        |
        | the proxy is asked to capture an app with a space in it              |
